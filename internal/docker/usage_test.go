package docker

import (
	"testing"

	"github.com/moby/moby/client"
)

// A sweep reports the difference between this total before it ran and after it
// ran, rather than the sum of what it believed each object was worth. That only
// means anything if both ends are counted the same way: both are the daemon's
// own totals, which already leave out the layers images share and the build
// cache records shared with another, as `docker system df` does.

func diskUsageFixture() client.DiskUsageResult {
	return client.DiskUsageResult{
		Images:     client.ImagesDiskUsage{TotalCount: 2, TotalSize: 4096},
		Containers: client.ContainersDiskUsage{TotalCount: 2, TotalSize: 350},
		Volumes:    client.VolumesDiskUsage{TotalCount: 3, TotalSize: 700},
		BuildCache: client.BuildCacheDiskUsage{TotalCount: 3, TotalSize: 800},
	}
}

func TestUsageOfReadsTheDaemonsTotals(t *testing.T) {
	got := usageOf(diskUsageFixture())

	want := DiskUsage{
		ImagesCount: 2, ImagesBytes: 4096,
		ContainersCount: 2, ContainersBytes: 350,
		VolumesCount: 3, VolumesBytes: 700,
		CacheCount: 3, CacheBytes: 800,
	}
	if got != want {
		t.Errorf("usageOf() = %+v, want %+v", got, want)
	}
}

func TestDiskUsageTotalAddsEveryKindOfSpace(t *testing.T) {
	u := usageOf(diskUsageFixture())
	const want = 4096 + 350 + 700 + 800
	if got := u.Total(); got != want {
		t.Errorf("Total() = %d, want %d", got, want)
	}
}
