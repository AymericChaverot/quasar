package docker

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
)

// DiskUsage is a dashboard-friendly summary of Docker's disk consumption.
type DiskUsage struct {
	ImagesCount     int
	ImagesSizeGB    float64
	ContainersCount int
	VolumesCount    int
	VolumesSizeGB   float64
}

func (c *Client) DiskUsage(ctx context.Context) (DiskUsage, error) {
	var out DiskUsage
	du, err := c.api.DiskUsage(ctx, types.DiskUsageOptions{})
	if err != nil {
		return out, err
	}
	out.ImagesCount = len(du.Images)
	out.ImagesSizeGB = float64(du.LayersSize) / (1 << 30)
	out.ContainersCount = len(du.Containers)
	out.VolumesCount = len(du.Volumes)
	for _, v := range du.Volumes {
		if v.UsageData != nil && v.UsageData.Size > 0 {
			out.VolumesSizeGB += float64(v.UsageData.Size) / (1 << 30)
		}
	}
	return out, nil
}

// SystemContainer is one of Quasar's own infrastructure containers, listed
// read-only on the dashboard.
type SystemContainer struct {
	Name   string
	Image  string
	State  string // "running", "exited", ...
	Uptime string
}

// SystemContainers lists the platform's own containers (dashboard, Traefik,
// socket proxy, updater) by their quasar- name prefix.
func (c *Client) SystemContainers(ctx context.Context) []SystemContainer {
	list, err := c.api.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: filters.NewArgs(filters.Arg("name", "quasar-")),
	})
	if err != nil {
		return nil
	}
	var out []SystemContainer
	for _, ct := range list {
		name := ""
		if len(ct.Names) > 0 {
			name = strings.TrimPrefix(ct.Names[0], "/")
		}
		sc := SystemContainer{Name: name, Image: ct.Image, State: ct.State}
		if ct.State == "running" {
			sc.Uptime = humanDuration(time.Since(time.Unix(ct.Created, 0)))
		}
		out = append(out, sc)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// GetSystemContainer returns details for a single quasar-* container,
// identified by its exact name. It rejects anything outside that name
// prefix, since this backs the read-only system-container detail view and
// must never resolve to an app or arbitrary container.
func (c *Client) GetSystemContainer(ctx context.Context, name string) (SystemContainer, error) {
	if !strings.HasPrefix(name, "quasar-") {
		return SystemContainer{}, fmt.Errorf("not a system container")
	}
	info, err := c.api.ContainerInspect(ctx, name)
	if err != nil {
		return SystemContainer{}, err
	}
	sc := SystemContainer{
		Name:  strings.TrimPrefix(info.Name, "/"),
		Image: info.Config.Image,
		State: info.State.Status,
	}
	if info.State.Running {
		if started, err := time.Parse(time.RFC3339Nano, info.State.StartedAt); err == nil {
			sc.Uptime = humanDuration(time.Since(started))
		}
	}
	return sc, nil
}

// EngineInfo reports the versions of the container tooling in use.
type EngineInfo struct {
	DockerVersion string
	APIVersion    string
	OSType        string
	TraefikImage  string
}

func (c *Client) EngineInfo(ctx context.Context) EngineInfo {
	out := EngineInfo{DockerVersion: "unknown", APIVersion: "unknown"}
	if v, err := c.api.ServerVersion(ctx); err == nil {
		out.DockerVersion = v.Version
		out.APIVersion = v.APIVersion
		out.OSType = v.Os + "/" + v.Arch
	}
	if info, err := c.api.ContainerInspect(ctx, "quasar-traefik"); err == nil {
		out.TraefikImage = info.Config.Image
	}
	return out
}

// PruneImages removes dangling images and reports the space reclaimed in MB.
func (c *Client) PruneImages(ctx context.Context) (float64, error) {
	report, err := c.api.ImagesPrune(ctx, filters.NewArgs(filters.Arg("dangling", "true")))
	if err != nil {
		return 0, err
	}
	return float64(report.SpaceReclaimed) / (1 << 20), nil
}
