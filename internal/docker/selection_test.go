package docker

import (
	"testing"

	"github.com/docker/docker/api/types/build"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/volume"
)

// A selection decides what gets deleted, so every one of these is a test about
// not deleting the wrong thing. The two directions matter equally: a sweep that
// takes more than was ticked destroys something the operator wanted, and one
// that takes less quietly does nothing while reporting success.

// The scan a narrowing is applied to: one of everything, so a rule that leaks
// between categories has somewhere to show up.
func fullSweep() sweep {
	return sweep{
		images:     []*image.Summary{{ID: "sha256:img1"}, {ID: "sha256:img2"}},
		dangling:   []*image.Summary{{ID: "sha256:dang1"}},
		cache:      []*build.CacheRecord{{ID: "cache1"}, {ID: "cache2"}},
		containers: []*container.Summary{ct("stray-1", "exited", 1, nil), ct("stray-2", "exited", 2, nil)},
		networks:   []string{"net-a", "net-b"},
		volumes:    []*volume.Volume{{Name: "vol-a"}, {Name: "vol-b"}},
	}
}

func TestParseSelectionDropsWhatItCannotRead(t *testing.T) {
	sel := ParseSelection([]string{
		"images:sha256:img1", // an image ID has colons of its own
		"dangling:*",
		"", "novalue:", ":noKey", "nocolon",
	})

	if !sel.objects["images:sha256:img1"] {
		t.Error("an image ID with colons in it was not kept whole")
	}
	if !sel.categories["dangling"] {
		t.Error("a whole-category tick was not read")
	}
	if len(sel.objects) != 1 || len(sel.categories) != 1 {
		t.Errorf("malformed values were kept: %+v", sel)
	}
}

// Nothing ticked is the whole scan — that is what the button has always done,
// and what the card still says it does.
func TestNarrowWithNothingTickedTakesEverythingButVolumes(t *testing.T) {
	got := CleanupOptions{}.narrow(fullSweep())

	if len(got.images) != 2 || len(got.dangling) != 1 || len(got.cache) != 2 ||
		len(got.containers) != 2 || len(got.networks) != 2 {
		t.Errorf("an empty selection did not take the whole scan: %+v", got)
	}
	// The one category a sweep never reaches on its own.
	if len(got.volumes) != 0 {
		t.Errorf("volumes were swept without consent: %v", got.volumes)
	}

	withVolumes := CleanupOptions{Volumes: true}.narrow(fullSweep())
	if len(withVolumes.volumes) != 2 {
		t.Errorf("volumes = %d with consent given, want 2", len(withVolumes.volumes))
	}
}

func TestNarrowTakesOnlyWhatWasTicked(t *testing.T) {
	got := CleanupOptions{Only: ParseSelection([]string{
		"images:sha256:img2",
		"containers:stray-1",
	})}.narrow(fullSweep())

	if len(got.images) != 1 || got.images[0].ID != "sha256:img2" {
		t.Errorf("images = %+v, want only img2", got.images)
	}
	if len(got.containers) != 1 || got.containers[0].ID != "stray-1" {
		t.Errorf("containers = %+v, want only stray-1", got.containers)
	}
	// Categories nobody ticked are not swept along with the ones that were.
	if len(got.dangling) != 0 || len(got.cache) != 0 || len(got.networks) != 0 || len(got.volumes) != 0 {
		t.Errorf("a narrowed sweep reached untouched categories: %+v", got)
	}
}

func TestNarrowTakesAWholeTickedCategory(t *testing.T) {
	got := CleanupOptions{Only: ParseSelection([]string{"dangling:*"})}.narrow(fullSweep())

	if len(got.dangling) != 1 {
		t.Errorf("dangling = %d, want the whole category", len(got.dangling))
	}
	if len(got.images) != 0 {
		t.Errorf("ticking dangling took tagged images too: %+v", got.images)
	}
}

// The build cache has no per-record removal, so a line of it is not a thing
// that can be ticked — only the category is.
func TestNarrowTakesTheBuildCacheOnlyAsAWhole(t *testing.T) {
	if got := (CleanupOptions{Only: ParseSelection([]string{"cache:cache1"})}).narrow(fullSweep()); len(got.cache) != 0 {
		t.Errorf("one cache record was ticked and %d were swept; the category is all or nothing", len(got.cache))
	}
	if got := (CleanupOptions{Only: ParseSelection([]string{"cache:*"})}).narrow(fullSweep()); len(got.cache) != 2 {
		t.Errorf("cache = %d with the category ticked, want 2", len(got.cache))
	}
}

// A volume ticked by name is consent for that volume, and for no other. This is
// the one category where getting it wrong destroys data no backup covers.
func TestNarrowTakesOnlyTheVolumesTicked(t *testing.T) {
	got := CleanupOptions{Only: ParseSelection([]string{"volumes:vol-b"})}.narrow(fullSweep())

	if len(got.volumes) != 1 || got.volumes[0].Name != "vol-b" {
		t.Errorf("volumes = %+v, want only vol-b", got.volumes)
	}

	// The consent above the list means all of them, whatever is ticked below.
	all := CleanupOptions{Volumes: true, Only: ParseSelection([]string{"volumes:vol-b"})}.narrow(fullSweep())
	if len(all.volumes) != 2 {
		t.Errorf("volumes = %d with the consent box ticked, want both", len(all.volumes))
	}
}

// A category ticked whole must stay whole even as it grows: the scan the page
// was drawn from is not the scan the sweep resolves, and a deploy in between
// leaves new layers behind.
func TestAWholeCategoryCoversWhatArrivedAfterTheScan(t *testing.T) {
	later := fullSweep()
	later.dangling = append(later.dangling, &image.Summary{ID: "sha256:dang2"})

	got := CleanupOptions{Only: ParseSelection([]string{"dangling:*"})}.narrow(later)
	if len(got.dangling) != 2 {
		t.Errorf("dangling = %d, want both including the one that arrived later", len(got.dangling))
	}
}
