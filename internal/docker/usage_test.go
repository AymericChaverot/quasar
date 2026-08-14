package docker

import (
	"testing"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/build"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/volume"
)

// A sweep now reports the difference between this total before it ran and after
// it ran, rather than the sum of what it believed each object was worth. That
// only means anything if both ends are counted the same way, so the arithmetic
// lives in one place and is checked here.

func diskUsageFixture() types.DiskUsage {
	return types.DiskUsage{
		// LayersSize is the daemon's own deduplicated figure for every image
		// layer on disk, which is why images are not summed here.
		LayersSize: 4096,
		Images: []*image.Summary{
			{ID: "sha256:a", Size: 3000, SharedSize: 1000},
			{ID: "sha256:b", Size: 2096, SharedSize: 1000},
		},
		Containers: []*container.Summary{
			{ID: "one", SizeRw: 100},
			{ID: "two", SizeRw: 250},
		},
		Volumes: []*volume.Volume{
			{Name: "kept", UsageData: &volume.UsageData{Size: 700}},
			// The daemon reports -1 when it did not work a volume's size out,
			// which must not be subtracted from the total.
			{Name: "unmeasured", UsageData: &volume.UsageData{Size: -1}},
			{Name: "unknown"},
		},
		BuildCache: []*build.CacheRecord{
			{ID: "c1", Size: 500, InUse: true},
			{ID: "c2", Size: 300},
			// Shared records are already counted under another record; charging
			// them again is how `docker system df` would overstate the total.
			{ID: "c3", Size: 900, Shared: true},
		},
	}
}

func TestUsageOfCountsEachThingOnce(t *testing.T) {
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

// The figure a sweep reports is a subtraction, so what matters is that removing
// things moves the total down by exactly what they were holding.
func TestTotalFallsByWhatWasRemoved(t *testing.T) {
	before := usageOf(diskUsageFixture())

	after := diskUsageFixture()
	after.LayersSize = 1096                 // an image worth 3000 of layers went
	after.Containers = nil                  // and both containers, worth 350
	after.BuildCache = after.BuildCache[:1] // leaving only the in-use record

	if got, want := before.Total()-usageOf(after).Total(), int64(3000+350+300); got != want {
		t.Errorf("freed = %d, want %d", got, want)
	}
}
