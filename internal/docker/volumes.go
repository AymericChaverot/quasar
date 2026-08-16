package docker

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/volume"

	"quasar/internal/db"
)

// Volume is one Docker volume, as the System page lists it.
type Volume struct {
	Name       string
	Driver     string
	Mountpoint string // the path on the host, which is what the browser opens
	Bytes      int64
	RefCount   int // how many containers hold it; 0 is what makes one orphaned
	Project    string
	AppID      string // the Quasar app it belongs to, empty when it belongs to none
	Created    time.Time
}

// InUse reports whether a container still references the volume. A volume that
// is not is the kind the cleanup card offers to delete, and the kind worth
// looking inside before agreeing to.
func (v Volume) InUse() bool { return v.RefCount > 0 }

// Local reports whether the volume's data sits on this machine's filesystem,
// which is what makes its contents browsable. A volume on an NFS or cloud
// driver has a mountpoint the dashboard cannot read through /host/root.
func (v Volume) Local() bool { return v.Driver == "local" && v.Mountpoint != "" }

// Volumes lists every volume on the server with its size and how many
// containers hold it.
//
// It goes through /system/df rather than /volumes for the same reason the
// cleanup scan does: that is the only endpoint where the daemon fills in
// UsageData, and asking once keeps this list and the reclaimable card counting
// the same bytes. The trade is that it is a slow call, which is why the section
// that shows it is a partial.
func (c *Client) Volumes(ctx context.Context) ([]Volume, error) {
	du, err := c.api.DiskUsage(ctx, types.DiskUsageOptions{})
	if err != nil {
		return nil, err
	}
	out := make([]Volume, 0, len(du.Volumes))
	for _, v := range du.Volumes {
		out = append(out, volumeFrom(v))
	}
	// Largest first: the reason to open this list is almost always to find what
	// is taking the disk.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Bytes != out[j].Bytes {
			return out[i].Bytes > out[j].Bytes
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// Volume resolves one volume by name, without the disk report.
//
// The explorer only needs the mountpoint, and paying for /system/df — seconds
// of the daemon sizing every layer on the server — on each step through a
// directory tree would make the browser crawl. Size is left at zero here; the
// caller that wants it is the list, not the browser.
func (c *Client) Volume(ctx context.Context, name string) (Volume, error) {
	v, err := c.api.VolumeInspect(ctx, name)
	if err != nil {
		return Volume{}, err
	}
	return volumeFrom(&v), nil
}

func volumeFrom(v *volume.Volume) Volume {
	out := Volume{
		Name:       v.Name,
		Driver:     v.Driver,
		Mountpoint: v.Mountpoint,
		Project:    v.Labels["com.docker.compose.project"],
	}
	if v.UsageData != nil {
		out.Bytes = v.UsageData.Size
		// The daemon reports -1 when it did not count, which must not read as
		// "held by minus one container".
		if v.UsageData.RefCount > 0 {
			out.RefCount = int(v.UsageData.RefCount)
		}
	}
	if t, err := time.Parse(time.RFC3339, v.CreatedAt); err == nil {
		out.Created = t
	}
	out.AppID = appIDFromProject(out.Project)
	// A volume created before its stack was labelled, or by a compose file that
	// names the project itself, still carries the project in its own name.
	if out.AppID == "" {
		out.AppID = appIDFromProject(volumeProject(v.Name))
	}
	return out
}

// appIDFromProject recovers the app ID from a compose project name, or returns
// empty for a project Quasar did not create.
func appIDFromProject(project string) string {
	if project == "" {
		return ""
	}
	id, ok := strings.CutPrefix(project, "qs-")
	if !ok {
		return ""
	}
	return id
}

// volumeProject reads the project out of a compose-created volume name, which
// is "<project>_<volume>". It is a guess — an operator may name a volume with
// an underscore in it — so it is only consulted when the label is missing, and
// only ever used to attribute a volume to an app in the listing.
func volumeProject(name string) string {
	i := strings.LastIndex(name, "_")
	if i <= 0 {
		return ""
	}
	return name[:i]
}

// Mount is one path an app's container has mounted from the host.
type Mount struct {
	Type        string // "volume" or "bind"
	Name        string // the volume's name; empty for a bind mount
	Source      string // where the data is on the host
	Destination string // where the container sees it
	RW          bool
	Service     string // the compose service that declared it, for a stack
}

// IsVolume reports whether this mount is a named Docker volume rather than a
// bind of a host path.
func (m Mount) IsVolume() bool { return m.Type == "volume" }

// AppMounts lists the storage an app's containers hold, deduplicated across the
// services of a stack.
//
// It inspects containers rather than reading the compose file, so it reports
// what is actually mounted now — including the volumes an image declares for
// itself, which no file on disk mentions and which are exactly the ones that
// quietly hold a database nobody knew was there.
//
// Stopped containers count. An app that will not start is the case where
// looking at its data matters most, and the daemon still knows what it had
// mounted.
func (c *Client) AppMounts(ctx context.Context, a *db.App) []Mount {
	list := c.appContainers(ctx, a.ID)
	if c.UsesCompose(a) {
		list = c.composeContainers(ctx, a.ID)
	}
	seen := map[string]bool{}
	var out []Mount
	for _, ct := range list {
		info, err := c.api.ContainerInspect(ctx, ct.ID)
		if err != nil {
			continue
		}
		for _, m := range info.Mounts {
			if skipMount(string(m.Type), m.Source) {
				continue
			}
			key := string(m.Type) + "\x00" + m.Source + "\x00" + m.Destination
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, Mount{
				Type:        string(m.Type),
				Name:        m.Name,
				Source:      m.Source,
				Destination: m.Destination,
				RW:          m.RW,
				Service:     ct.Labels["com.docker.compose.service"],
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Destination < out[j].Destination })
	return out
}

// skipMount drops the mounts there is nothing to browse in.
//
// tmpfs has no source on the host at all. The Docker socket is not data, and a
// stack that binds it has handed its container the daemon — offering to open it
// in a file browser is noise at best.
func skipMount(kind, source string) bool {
	if source == "" || kind == "tmpfs" || kind == "npipe" {
		return true
	}
	return strings.HasSuffix(source, "/docker.sock")
}
