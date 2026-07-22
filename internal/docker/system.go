package docker

import (
	"context"

	"github.com/docker/docker/api/types"
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

// PruneImages removes dangling images and reports the space reclaimed in MB.
func (c *Client) PruneImages(ctx context.Context) (float64, error) {
	report, err := c.api.ImagesPrune(ctx, filters.NewArgs(filters.Arg("dangling", "true")))
	if err != nil {
		return 0, err
	}
	return float64(report.SpaceReclaimed) / (1 << 20), nil
}
