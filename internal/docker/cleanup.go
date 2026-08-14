package docker

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/build"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"
)

// Reclaimable is one category of Docker state a sweep would remove.
type Reclaimable struct {
	Key   string // stable identifier; the page picks the row's icon from it
	Label string
	Count int
	Bytes int64
	Note  string // a few of the actual names, so the count is not the only thing shown
}

// CleanupScan is the preview of a sweep: what would go and what it is worth,
// worked out without removing anything.
//
// Showing this before the button is pressed is most of the point. "Prune
// dangling images" is a promise the operator cannot check, and on a server that
// has been deploying for months the honest answer — several gigabytes — is the
// only thing that makes the button worth pressing.
type CleanupScan struct {
	Items   []Reclaimable // categories safe to remove, in display order
	Volumes Reclaimable   // orphaned volumes: data loss, so opted into separately
	Count   int           // objects across Items
	Bytes   int64         // space across Items
}

// Empty reports whether a sweep would do nothing at all.
func (s CleanupScan) Empty() bool { return s.Count == 0 && s.Volumes.Count == 0 }

// CleanupReport is what a sweep actually removed.
type CleanupReport struct {
	Images     int
	Cache      int
	Containers int
	Networks   int
	Volumes    int
	Bytes      int64
	Failed     int // objects the daemon refused to remove
}

// Summary is the sentence the System page shows after a sweep.
func (r CleanupReport) Summary() string {
	var parts []string
	for _, p := range []struct {
		n    int
		one  string
		many string
	}{
		{r.Images, "image", "images"},
		{r.Cache, "build cache entry", "build cache entries"},
		{r.Containers, "container", "containers"},
		{r.Networks, "network", "networks"},
		{r.Volumes, "volume", "volumes"},
	} {
		if p.n == 0 {
			continue
		}
		word := p.many
		if p.n == 1 {
			word = p.one
		}
		parts = append(parts, fmt.Sprintf("%d %s", p.n, word))
	}
	if len(parts) == 0 {
		return "Nothing to clean up — Docker is already using only what it needs."
	}
	msg := fmt.Sprintf("Removed %s, freeing about %s.", strings.Join(parts, ", "), HumanSize(r.Bytes))
	if r.Failed == 1 {
		msg += " One object was still in use and was left alone."
	} else if r.Failed > 1 {
		msg += fmt.Sprintf(" %d objects were still in use and were left alone.", r.Failed)
	}
	return msg
}

// keep holds what a sweep must not touch.
//
// Everything Quasar can still act on has to survive: the container a stopped
// app is started from, the image a rollback would redeploy, the network a stack
// expects to find when it comes back up. The rules are gathered here rather
// than repeated per category, because a sweep is only as safe as its least
// careful exclusion.
type keep struct {
	apps     map[string]bool // app IDs the platform still knows about
	builds   map[string]bool // local build repositories of those apps
	projects map[string]bool // compose project names of those apps
	networks map[string]bool // networks the platform owns
	current  map[string]bool // container IDs an app is currently deployed as
}

func (c *Client) protect(appIDs []string, containers []*container.Summary) keep {
	k := keep{
		apps:     map[string]bool{},
		builds:   map[string]bool{},
		projects: map[string]bool{},
		networks: map[string]bool{c.network: true, c.socketNet: true},
		current:  map[string]bool{},
	}
	for _, id := range appIDs {
		k.apps[id] = true
		k.builds[buildTagPrefix(id)] = true
		k.projects[composeProject(id)] = true
	}

	// A deploy starts the replacement container before retiring the one it
	// replaces, so an app can own several containers at once and the newest is
	// the one every action resolves to. That newest container is what a stopped
	// app is started from and must survive; its predecessors are dead weight an
	// interrupted deploy left behind.
	newest := map[string]*container.Summary{}
	for _, ct := range containers {
		id := ct.Labels[appLabel]
		if id == "" || !k.apps[id] {
			continue
		}
		if cur, ok := newest[id]; !ok || ct.Created > cur.Created {
			newest[id] = ct
		}
	}
	for _, ct := range newest {
		k.current[ct.ID] = true
	}
	return k
}

func (k keep) container(ct *container.Summary) bool {
	switch ct.State {
	case "running", "paused", "restarting", "removing", "created":
		// "created" covers the few seconds between ContainerCreate and
		// ContainerStart during a deploy, which is exactly when a sweep would
		// be most damaging.
		return true
	}
	// The platform's own containers, including the exited quasar-updater a
	// self-update deliberately leaves behind as its only post-mortem record.
	for _, name := range ct.Names {
		if strings.HasPrefix(strings.TrimPrefix(name, "/"), "quasar-") {
			return true
		}
	}
	if k.current[ct.ID] {
		return true
	}
	// A compose stack is managed as a whole: a service of it that happens to be
	// stopped is part of the app, not a leftover.
	return k.projects[ct.Labels["com.docker.compose.project"]]
}

func (k keep) image(img *image.Summary) bool {
	// Containers counts stopped containers too, which is what makes a stopped
	// app's image safe from here.
	if img.Containers != 0 {
		return true
	}
	// Git builds are tagged qs-<app>:<unix>, and the last few of them are what a
	// rollback redeploys — deploy already caps how many are kept. Removing them
	// here would quietly take the rollback targets with them. Only while the app
	// exists, though: once it is deleted nothing can ever redeploy them, and
	// they are among the largest things left on a build server.
	for _, tag := range img.RepoTags {
		if repo, _, ok := strings.Cut(tag, ":"); ok && k.builds[repo] {
			return true
		}
	}
	return false
}

func (k keep) volume(v *volume.Volume) bool {
	// RefCount counts stopped containers, and -1 means the daemon did not work
	// it out — neither is a volume anyone may delete.
	if v.UsageData == nil || v.UsageData.RefCount != 0 {
		return true
	}
	return k.projects[v.Labels["com.docker.compose.project"]]
}

func (k keep) network(n network.Summary) bool {
	// The three networks every daemon is born with cannot be removed anyway.
	switch n.Name {
	case "bridge", "host", "none":
		return true
	}
	if n.Scope != "local" || k.networks[n.Name] || strings.HasPrefix(n.Name, "quasar-") {
		return true
	}
	// A compose network is recreated on the next `up`, but only if compose still
	// has an app to bring up. Network inspection reports connected containers,
	// and a stopped container is not connected — without this the network of
	// every stopped stack would look abandoned.
	return k.projects[n.Labels["com.docker.compose.project"]]
}

// sweep is everything a cleanup would remove, resolved in one pass so the
// preview on the page and the removal the button performs cannot disagree.
type sweep struct {
	images     []*image.Summary
	dangling   []*image.Summary
	cache      []*build.CacheRecord
	containers []*container.Summary
	networks   []string
	volumes    []*volume.Volume
	usage      DiskUsage
}

// resolve works out what Docker is holding and which of it is disposable.
//
// It leans on /system/df for everything but networks: that is the one endpoint
// where the daemon fills in the reference counts and shared-layer sizes this
// has to be correct about, and doing it in a single call keeps the preview
// consistent with itself.
func (c *Client) resolve(ctx context.Context, appIDs []string) (sweep, error) {
	var s sweep
	du, err := c.api.DiskUsage(ctx, types.DiskUsageOptions{})
	if err != nil {
		return s, err
	}
	k := c.protect(appIDs, du.Containers)
	s.usage = usageOf(du)

	for _, img := range du.Images {
		if k.image(img) {
			continue
		}
		if untagged(img) {
			s.dangling = append(s.dangling, img)
		} else {
			s.images = append(s.images, img)
		}
	}
	for _, rec := range du.BuildCache {
		if !rec.Shared && !rec.InUse {
			s.cache = append(s.cache, rec)
		}
	}
	for _, ct := range du.Containers {
		if !k.container(ct) {
			s.containers = append(s.containers, ct)
		}
	}
	for _, v := range du.Volumes {
		if !k.volume(v) {
			s.volumes = append(s.volumes, v)
		}
	}
	s.networks = c.strayNetworks(ctx, k)
	return s, nil
}

// usageOf reduces the daemon's disk report to the totals the System page shows.
// Both the scan and the measurement of what a sweep freed go through it, so the
// before and after of a sweep are counted the same way and their difference
// means something.
func usageOf(du types.DiskUsage) DiskUsage {
	u := DiskUsage{
		ImagesCount:     len(du.Images),
		ImagesBytes:     du.LayersSize,
		ContainersCount: len(du.Containers),
		VolumesCount:    len(du.Volumes),
		CacheCount:      len(du.BuildCache),
	}
	for _, rec := range du.BuildCache {
		// A shared record is one the daemon also counts under another; adding it
		// here would charge the same layer to the total twice. This is the same
		// arithmetic `docker system df` does.
		if !rec.Shared {
			u.CacheBytes += rec.Size
		}
	}
	for _, ct := range du.Containers {
		u.ContainersBytes += ct.SizeRw
	}
	for _, v := range du.Volumes {
		if v.UsageData != nil && v.UsageData.Size > 0 {
			u.VolumesBytes += v.UsageData.Size
		}
	}
	return u
}

// strayNetworks lists local networks nothing is attached to and the platform
// does not own. Each candidate is inspected because a network listing does not
// report its endpoints, and a network wrongly judged unused takes a live
// application off the internet.
func (c *Client) strayNetworks(ctx context.Context, k keep) []string {
	list, err := c.api.NetworkList(ctx, network.ListOptions{})
	if err != nil {
		return nil
	}
	var out []string
	for _, n := range list {
		if k.network(n) {
			continue
		}
		full, err := c.api.NetworkInspect(ctx, n.ID, network.InspectOptions{})
		if err != nil || len(full.Containers) > 0 {
			continue
		}
		out = append(out, n.Name)
	}
	sort.Strings(out)
	return out
}

// untagged reports whether an image is only reachable by its digest — the
// `<none>` layers a rebuild leaves behind when it moves a tag onto a new image.
func untagged(img *image.Summary) bool {
	for _, tag := range img.RepoTags {
		if tag != "<none>:<none>" {
			return false
		}
	}
	return true
}

// firstTag names a tagged image for the operator. An image can hold a `<none>`
// entry alongside real ones, and that is the one name that says nothing.
func firstTag(img *image.Summary) string {
	for _, tag := range img.RepoTags {
		if tag != "<none>:<none>" {
			return tag
		}
	}
	return img.ID
}

// imageBytes is what removing an image actually gives back: its own layers,
// minus the ones another image still holds.
func imageBytes(img *image.Summary) int64 {
	if img.SharedSize > 0 {
		return img.Size - img.SharedSize
	}
	return img.Size
}

// Storage is the disk picture the System page shows: what Docker is using, and
// how much of it a sweep would give back.
type Storage struct {
	Usage   DiskUsage
	Cleanup CleanupScan
}

func (c *Client) Storage(ctx context.Context, appIDs []string) (Storage, error) {
	s, err := c.resolve(ctx, appIDs)
	if err != nil {
		return Storage{}, err
	}
	return Storage{Usage: s.usage, Cleanup: s.scan()}, nil
}

func (s sweep) scan() CleanupScan {
	var out CleanupScan

	add := func(r Reclaimable) {
		if r.Count == 0 {
			return
		}
		out.Items = append(out.Items, r)
		out.Count += r.Count
		out.Bytes += r.Bytes
	}

	var names []string
	var bytes int64
	for _, img := range s.images {
		names = append(names, firstTag(img))
		bytes += imageBytes(img)
	}
	add(Reclaimable{
		Key:   "images",
		Label: "Images no container uses",
		Count: len(s.images),
		Bytes: bytes,
		Note:  examples(names),
	})

	bytes = 0
	for _, img := range s.dangling {
		bytes += imageBytes(img)
	}
	add(Reclaimable{
		Key:   "dangling",
		Label: "Untagged layers left by rebuilds",
		Count: len(s.dangling),
		Bytes: bytes,
	})

	bytes = 0
	for _, rec := range s.cache {
		bytes += rec.Size
	}
	add(Reclaimable{
		Key:   "cache",
		Label: "Build cache no longer referenced",
		Count: len(s.cache),
		Bytes: bytes,
	})

	names, bytes = nil, 0
	for _, ct := range s.containers {
		if len(ct.Names) > 0 {
			names = append(names, strings.TrimPrefix(ct.Names[0], "/"))
		}
		bytes += ct.SizeRw
	}
	add(Reclaimable{
		Key:   "containers",
		Label: "Stopped containers nothing owns",
		Count: len(s.containers),
		Bytes: bytes,
		Note:  examples(names),
	})

	add(Reclaimable{
		Key:   "networks",
		Label: "Networks with nothing attached",
		Count: len(s.networks),
		Note:  examples(s.networks),
	})

	names, bytes = nil, 0
	for _, v := range s.volumes {
		names = append(names, v.Name)
		if v.UsageData != nil {
			bytes += v.UsageData.Size
		}
	}
	out.Volumes = Reclaimable{
		Key:   "volumes",
		Label: "Volumes no container references",
		Count: len(s.volumes),
		Bytes: bytes,
		Note:  examples(names),
	}
	return out
}

// examples names the first few objects in a category. A count alone asks the
// operator to trust the tool; three names let them recognise what is about to
// go and stop if it is something they wanted.
func examples(names []string) string {
	sort.Strings(names)
	const show = 3
	if len(names) <= show {
		return strings.Join(names, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(names[:show], ", "), len(names)-show)
}

// cleanupPasses bounds how many times a sweep looks again at what is left.
//
// Removal cascades. The container removed in one pass is what frees the image
// it was created from; the image removed by its last tag is what turns its
// layers into the untagged leftovers of the next. A single pass therefore
// always stops short of what it set out to do, and the page it returns to says
// so — a sweep reporting success over a breakdown still offering a few hundred
// megabytes, which is the state this bound exists to clear.
//
// Four is past the depth of that chain (containers, their images, the layers
// those images held). The loop stops early on the pass that removes nothing,
// so the bound is only ever reached by a daemon that keeps finding more.
const cleanupPasses = 4

// Cleanup removes everything a scan found disposable, and the orphaned volumes
// too if the operator asked for those separately. It repeats until a pass finds
// nothing left to take, so what the operator is shown afterwards is a sweep
// that finished rather than one that ran out of turns.
//
// Every removal is attempted on its own rather than through Docker's own prune
// endpoints, which take no exclusions: the whole difference between this and
// `docker system prune -a` is what it refuses to touch.
func (c *Client) Cleanup(ctx context.Context, appIDs []string, withVolumes bool) (CleanupReport, error) {
	before, err := c.api.DiskUsage(ctx, types.DiskUsageOptions{})
	if err != nil {
		return CleanupReport{}, err
	}

	var rep CleanupReport
	for pass := 0; pass < cleanupPasses; pass++ {
		s, err := c.resolve(ctx, appIDs)
		if err != nil {
			if pass == 0 {
				return CleanupReport{}, err
			}
			break // what the earlier passes removed still stands, and is reported
		}
		removed, failed := c.sweepOnce(ctx, s, withVolumes, &rep)
		// Only the latest pass's failures are kept: an object an earlier pass
		// could not remove was tried again, and either went or is counted here.
		// Summing them would report one stubborn image as four.
		rep.Failed = failed
		if removed == 0 {
			break
		}
	}

	// Measured rather than added up. The per-object sizes are estimates that
	// double-count shared layers and credit an image whose removal only dropped
	// one of its tags, which is how a sweep came to claim more than the disk
	// gave back. A negative difference means something else grew while this ran
	// — a deploy, most likely — and is not this sweep's to report.
	if after, err := c.api.DiskUsage(ctx, types.DiskUsageOptions{}); err == nil {
		if freed := usageOf(before).Total() - usageOf(after).Total(); freed > 0 {
			rep.Bytes = freed
		}
	}
	return rep, nil
}

// sweepOnce removes everything one resolution found disposable, adding what
// went to rep. It returns how much it removed and how much the daemon refused,
// which is what tells the caller whether another pass is worth making.
func (c *Client) sweepOnce(ctx context.Context, s sweep, withVolumes bool, rep *CleanupReport) (removed, failed int) {
	for _, ct := range s.containers {
		if err := c.api.ContainerRemove(ctx, ct.ID, container.RemoveOptions{}); err != nil {
			failed++
			continue
		}
		rep.Containers++
		removed++
	}

	for _, img := range append(append([]*image.Summary{}, s.images...), s.dangling...) {
		if c.removeImage(ctx, img) {
			rep.Images++
			removed++
			continue
		}
		failed++
	}

	// The build cache has no per-record delete, and its own prune already keeps
	// what is in use — the same set the scan counted.
	if len(s.cache) > 0 {
		if out, err := c.api.BuildCachePrune(ctx, build.CachePruneOptions{All: true}); err == nil {
			rep.Cache += len(out.CachesDeleted)
			removed += len(out.CachesDeleted)
		} else {
			failed++
		}
	}

	for _, name := range s.networks {
		if err := c.api.NetworkRemove(ctx, name); err != nil {
			failed++
			continue
		}
		rep.Networks++
		removed++
	}

	if withVolumes {
		for _, v := range s.volumes {
			if err := c.api.VolumeRemove(ctx, v.Name, false); err != nil {
				failed++
				continue
			}
			rep.Volumes++
			removed++
		}
	}
	return removed, failed
}

// removeImage deletes an image and reports whether the daemon took it. How it
// has to be asked depends on whether the image still carries a name.
func (c *Client) removeImage(ctx context.Context, img *image.Summary) bool {
	if untagged(img) {
		// An untagged image has no name to delete it by, only its ID — and the
		// daemon refuses an ID that is still referenced somewhere, which for one
		// of these is a digest left over from the pull or rebuild that untagged
		// it. That refusal is what kept "untagged layers left by rebuilds" on
		// the page after every sweep. Forcing is what `docker image prune`
		// itself does here, and it is safe for the same reason: an image any
		// container still uses never reaches this, keep.image holds it back.
		_, err := c.api.ImageRemove(ctx, img.ID, image.RemoveOptions{Force: true, PruneChildren: true})
		return err == nil
	}
	// A tagged image is deleted the way `docker rmi` does it: by each of its
	// tags, so the daemon drops the references first and the image with the
	// last one. Deleting a multi-tagged image by ID would need forcing, and
	// forcing it there would defeat the daemon's own last check that nothing is
	// using it.
	ok := false
	for _, ref := range img.RepoTags {
		if _, err := c.api.ImageRemove(ctx, ref, image.RemoveOptions{PruneChildren: true}); err == nil {
			ok = true
		}
	}
	return ok
}

// HumanSize formats a byte count the way the pages show sizes: never more than
// three significant figures, and never a unit the number does not fill.
func HumanSize(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit && exp < 3; n /= unit {
		div *= unit
		exp++
	}
	v := float64(b) / float64(div)
	if v >= 100 {
		return fmt.Sprintf("%.0f %cB", v, "KMGT"[exp])
	}
	return fmt.Sprintf("%.1f %cB", v, "KMGT"[exp])
}
