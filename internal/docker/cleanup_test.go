package docker

import (
	"testing"

	"github.com/docker/docker/api/types/build"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"
)

// The exclusions are the whole of a cleanup's safety: what it removes is
// recoverable by a pull or a rebuild, what it must not touch is not. These
// tests exercise the rules directly, because the only other way to find out
// that one of them is wrong is on a production server.

const (
	liveApp    = "beef0001" // deployed and running
	stoppedApp = "beef0003" // deployed, then stopped by the operator
	deadApp    = "dead0002" // deleted from the platform
)

func testClient() *Client {
	return &Client{network: "traefik-net", socketNet: "quasar-socket-net"}
}

func ct(name, state string, created int64, labels map[string]string) *container.Summary {
	return &container.Summary{
		ID:      name,
		Names:   []string{"/" + name},
		State:   state,
		Created: created,
		Labels:  labels,
	}
}

func TestKeepProtectsWhatThePlatformStillNeeds(t *testing.T) {
	serving := ct("qs-beef0001-300-dd", "running", 300, map[string]string{appLabel: liveApp})
	superseded := ct("qs-beef0001-100-bb", "exited", 100, map[string]string{appLabel: liveApp})
	// The one an operator pressed Stop on: exited, and the only thing Start has
	// left to work with.
	stopped := ct("qs-beef0003-200-aa", "exited", 200, map[string]string{appLabel: stoppedApp})
	orphan := ct("qs-dead0002-50-cc", "exited", 50, map[string]string{appLabel: deadApp})
	stack := ct("qs-beef0001-db-1", "exited", 90, map[string]string{"com.docker.compose.project": composeProject(liveApp)})
	deadStack := ct("qs-dead0002-db-1", "exited", 90, map[string]string{"com.docker.compose.project": composeProject(deadApp)})
	updater := ct("quasar-updater", "exited", 10, nil)
	starting := ct("qs-beef0001-400-ee", "created", 400, map[string]string{appLabel: liveApp})

	all := []*container.Summary{serving, superseded, stopped, orphan, stack, deadStack, updater, starting}
	k := testClient().protect([]string{liveApp, stoppedApp}, all)

	for _, tc := range []struct {
		name string
		ct   *container.Summary
		keep bool
	}{
		{"the container serving a live app", serving, true},
		{"the only container of a stopped app", stopped, true},
		{"a container created but not started yet", starting, true},
		{"a service of a live compose stack", stack, true},
		{"the platform's own updater", updater, true},
		{"a container superseded by a later deploy", superseded, false},
		{"the container of a deleted app", orphan, false},
		{"a stack whose app is gone", deadStack, false},
	} {
		if got := k.container(tc.ct); got != tc.keep {
			t.Errorf("%s: keep = %v, want %v", tc.name, got, tc.keep)
		}
	}
}

// A running container makes its app's newest container ambiguous only if
// Created is compared wrongly, so pin down that the newest wins regardless of
// the order the daemon happens to list them in.
func TestOnlyTheNewestContainerOfAnAppSurvives(t *testing.T) {
	newest := ct("new", "exited", 300, map[string]string{appLabel: liveApp})
	middle := ct("mid", "exited", 200, map[string]string{appLabel: liveApp})
	oldest := ct("old", "exited", 100, map[string]string{appLabel: liveApp})

	k := testClient().protect([]string{liveApp}, []*container.Summary{middle, oldest, newest})
	if !k.container(newest) {
		t.Error("the newest container of a live app must survive")
	}
	for _, c := range []*container.Summary{middle, oldest} {
		if k.container(c) {
			t.Errorf("%s: a superseded container is not worth keeping", c.ID)
		}
	}
}

func TestKeepProtectsRollbackBuildsOfLiveAppsOnly(t *testing.T) {
	k := testClient().protect([]string{liveApp}, nil)

	for _, tc := range []struct {
		name string
		img  *image.Summary
		keep bool
	}{
		{"an image a stopped container still needs",
			&image.Summary{Containers: 1, RepoTags: []string{"nginx:1.24"}}, true},
		{"a git build a rollback could redeploy",
			&image.Summary{RepoTags: []string{buildTagPrefix(liveApp) + ":1700000000"}}, true},
		{"a git build of a deleted app",
			&image.Summary{RepoTags: []string{buildTagPrefix(deadApp) + ":1700000000"}}, false},
		{"a registry image nothing runs",
			&image.Summary{RepoTags: []string{"redis:7"}}, false},
		{"an untagged layer",
			&image.Summary{RepoTags: []string{"<none>:<none>"}}, false},
	} {
		if got := k.image(tc.img); got != tc.keep {
			t.Errorf("%s: keep = %v, want %v", tc.name, got, tc.keep)
		}
	}
}

// -1 is how the daemon says it did not work the reference count out. Reading
// that as zero would delete volumes it never claimed were unused.
func TestKeepTreatsAnUncountedVolumeAsInUse(t *testing.T) {
	k := testClient().protect([]string{liveApp}, nil)

	for _, tc := range []struct {
		name string
		vol  *volume.Volume
		keep bool
	}{
		{"reference count not calculated",
			&volume.Volume{Name: "unknown", UsageData: &volume.UsageData{RefCount: -1}}, true},
		{"no usage data at all", &volume.Volume{Name: "none"}, true},
		{"still referenced",
			&volume.Volume{Name: "busy", UsageData: &volume.UsageData{RefCount: 1}}, true},
		{"belongs to a live stack", &volume.Volume{
			Name:      "qs-beef0001_pgdata",
			Labels:    map[string]string{"com.docker.compose.project": composeProject(liveApp)},
			UsageData: &volume.UsageData{RefCount: 0},
		}, true},
		{"orphaned", &volume.Volume{Name: "stray", UsageData: &volume.UsageData{RefCount: 0}}, false},
	} {
		if got := k.volume(tc.vol); got != tc.keep {
			t.Errorf("%s: keep = %v, want %v", tc.name, got, tc.keep)
		}
	}
}

// Network inspection reports connected containers, and a stopped container is
// not connected — so a live stack's network looks abandoned and is only saved
// by its project label.
func TestKeepProtectsPlatformAndLiveStackNetworks(t *testing.T) {
	k := testClient().protect([]string{liveApp}, nil)

	for _, tc := range []struct {
		name string
		net  network.Summary
		keep bool
	}{
		{"the default bridge", network.Summary{Name: "bridge", Scope: "local"}, true},
		{"the edge router's network", network.Summary{Name: "traefik-net", Scope: "local"}, true},
		{"the socket proxy's network", network.Summary{Name: "quasar-socket-net", Scope: "local"}, true},
		{"a live stack's network", network.Summary{
			Name:   "qs-beef0001_default",
			Scope:  "local",
			Labels: map[string]string{"com.docker.compose.project": composeProject(liveApp)},
		}, true},
		{"a swarm network this host does not own", network.Summary{Name: "elsewhere", Scope: "swarm"}, true},
		{"a deleted stack's network", network.Summary{
			Name:   "qs-dead0002_default",
			Scope:  "local",
			Labels: map[string]string{"com.docker.compose.project": composeProject(deadApp)},
		}, false},
	} {
		if got := k.network(tc.net); got != tc.keep {
			t.Errorf("%s: keep = %v, want %v", tc.name, got, tc.keep)
		}
	}
}

// With no applications, everything an application could own is fair game — so
// this is also the shape of the accident that would follow an unreadable app
// table, which is why the handler refuses to sweep on one.
func TestNoLiveAppsProtectsOnlyThePlatform(t *testing.T) {
	k := testClient().protect(nil, nil)
	if !k.container(ct("quasar-dashboard", "exited", 1, nil)) {
		t.Error("the platform's own containers are never anyone's leftovers")
	}
	if k.image(&image.Summary{RepoTags: []string{buildTagPrefix(liveApp) + ":1"}}) {
		t.Error("without a live app, a build tag protects nothing")
	}
}

func TestUntaggedRecognisesEveryFormOfDangling(t *testing.T) {
	for _, tc := range []struct {
		name string
		tags []string
		want bool
	}{
		{"no tags at all", nil, true},
		{"the daemon's placeholder", []string{"<none>:<none>"}, true},
		{"a real tag", []string{"nginx:1.24"}, false},
		{"a real tag beside the placeholder", []string{"<none>:<none>", "nginx:1.24"}, false},
	} {
		if got := untagged(&image.Summary{RepoTags: tc.tags}); got != tc.want {
			t.Errorf("%s: untagged = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// Sharing is why the total cannot just be the sum of image sizes: a 900 MB
// image on top of an 850 MB base gives back 50 MB, not 900.
func TestImageBytesDiscountsSharedLayers(t *testing.T) {
	for _, tc := range []struct {
		name string
		img  *image.Summary
		want int64
	}{
		{"layers shared with another image",
			&image.Summary{Size: 900, SharedSize: 850}, 50},
		{"nothing shared", &image.Summary{Size: 900, SharedSize: 0}, 900},
		{"sharing not calculated", &image.Summary{Size: 900, SharedSize: -1}, 900},
	} {
		if got := imageBytes(tc.img); got != tc.want {
			t.Errorf("%s: imageBytes = %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestHumanSizeFillsItsUnit(t *testing.T) {
	for _, tc := range []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.0 KB"},
		{1024 * 1024, "1.0 MB"},
		{40 * 1024 * 1024, "40.0 MB"},
		{512 * 1024 * 1024, "512 MB"},
		{3 * 1024 * 1024 * 1024, "3.0 GB"},
	} {
		if got := HumanSize(tc.in); got != tc.want {
			t.Errorf("HumanSize(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestReportSummaryNamesWhatWent(t *testing.T) {
	empty := CleanupReport{}.Summary()
	if empty == "" || !contains(empty, "Nothing to clean up") {
		t.Errorf("an empty sweep should say so, got %q", empty)
	}

	rep := CleanupReport{Images: 1, Containers: 3, Bytes: 2 * 1024 * 1024 * 1024, Failed: 2}
	got := rep.Summary()
	for _, want := range []string{"1 image", "3 containers", "2.0 GB", "2 objects"} {
		if !contains(got, want) {
			t.Errorf("summary %q is missing %q", got, want)
		}
	}
	// A sweep that finished must not hedge: "run it again" over a completed
	// cleanup is what sent the operator back to the button six times.
	if contains(got, "run it again") {
		t.Errorf("a finished sweep should not ask to be repeated, got %q", got)
	}

	partial := CleanupReport{Images: 1, Incomplete: true}.Summary()
	if !contains(partial, "run it again") {
		t.Errorf("a sweep that ran out of passes should say so, got %q", partial)
	}
}

// The pass that finds nothing is what ends a sweep, so empty has to be false
// for every category on its own — a category it forgot would end the loop with
// that category's objects still on the disk.
func TestSweepIsEmptyOnlyWhenNothingIsLeftToTake(t *testing.T) {
	if !(sweep{}).empty() {
		t.Fatal("a sweep with nothing in it should be empty")
	}
	img := &image.Summary{ID: "sha256:aa"}
	for name, s := range map[string]sweep{
		"images":     {images: []*image.Summary{img}},
		"dangling":   {dangling: []*image.Summary{img}},
		"cache":      {cache: []*build.CacheRecord{{ID: "c1"}}},
		"containers": {containers: []*container.Summary{ct("qs-x", "exited", 1, nil)}},
		"networks":   {networks: []string{"stray"}},
		"volumes":    {volumes: []*volume.Volume{{Name: "orphan"}}},
	} {
		if s.empty() {
			t.Errorf("a sweep holding %s is not empty", name)
		}
	}
	// The disk figures ride along on every resolution and are not things to
	// remove: a sweep carrying only those has nothing left to do.
	if !(sweep{usage: DiskUsage{ImagesCount: 12}}).empty() {
		t.Error("disk usage alone is not something to sweep")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
