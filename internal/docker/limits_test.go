package docker

import (
	"testing"

	"github.com/docker/docker/api/types/container"

	"quasar/internal/db"
)

func limitedApp(cpu float64, memMB int64) *db.App {
	return &db.App{ID: "a1", Subdomain: "app", Port: 8080, CPULimit: cpu, MemLimitMB: memMB}
}

// A deploy and a live change have to leave the container in the same place, or
// the panel comparing the two would report a redeploy owed forever.
func TestLiveLimitsMatchWhatADeployCreates(t *testing.T) {
	a := limitedApp(1.5, 512)
	created, updated := appResources(a), limitsUpdate(a)
	if created.NanoCPUs != updated.NanoCPUs {
		t.Errorf("created with %d nanoCPUs, updated to %d", created.NanoCPUs, updated.NanoCPUs)
	}
	if created.Memory != updated.Memory {
		t.Errorf("created with %d bytes, updated to %d", created.Memory, updated.Memory)
	}
	if created.Memory != 512<<20 || created.NanoCPUs != 1_500_000_000 {
		t.Errorf("limits are not in Docker's units: %d nanoCPUs, %d bytes", created.NanoCPUs, created.Memory)
	}
}

// The Engine refuses a memory limit that would fall below the swap total the
// container already carries, so lowering one without moving swap with it fails
// outright. Twice the limit is what creating the container produces.
func TestLiveMemoryLimitCarriesSwapWithIt(t *testing.T) {
	got := limitsUpdate(limitedApp(0, 512))
	if want := int64(2 * 512 << 20); got.MemorySwap != want {
		t.Errorf("MemorySwap = %d, want %d", got.MemorySwap, want)
	}
	// Unlimited memory must not name a swap total either: a swap limit with no
	// memory limit beside it is rejected.
	if got := limitsUpdate(limitedApp(2, 0)); got.MemorySwap != 0 {
		t.Errorf("an unlimited app asked for %d bytes of swap", got.MemorySwap)
	}
}

// Zero means "leave as it is" to the Engine, so a limit cannot be lifted in
// place. What must not happen is the change being reported as applied: the
// container really is still holding the old ceiling.
func TestLiftingALimitStaysPending(t *testing.T) {
	running := &container.HostConfig{Resources: container.Resources{NanoCPUs: 2e9, Memory: 512 << 20}}
	unlimited := limitedApp(0, 0)

	if update := limitsUpdate(unlimited); update.NanoCPUs != 0 || update.Memory != 0 {
		t.Errorf("removing a limit sent %+v, which the Engine reads as a change", update)
	}
	if limitsApplied(unlimited, running) {
		t.Error("an app with its limits removed is reported as applied while the container still holds them")
	}
	if !limitsApplied(limitedApp(2, 512), running) {
		t.Error("a container running exactly the stored limits is reported as out of step")
	}
}

// A container with no limits at all and an app with none agree — otherwise
// every unlimited application would show a redeploy it does not need.
func TestUnlimitedOnBothSidesAgrees(t *testing.T) {
	if !limitsApplied(limitedApp(0, 0), &container.HostConfig{}) {
		t.Error("an unlimited app on an unlimited container is reported as out of step")
	}
}

func TestLimitsText(t *testing.T) {
	tests := []struct {
		cpu   float64
		memMB int64
		want  string
	}{
		{0, 0, "unlimited"},
		{1, 0, "1 CPU"},
		{0.5, 0, "0.5 CPU"},
		{0, 512, "512 MB"},
		{2, 1024, "2 CPU · 1024 MB"},
	}
	for _, tc := range tests {
		if got := LimitsText(tc.cpu, tc.memMB); got != tc.want {
			t.Errorf("LimitsText(%v, %d) = %q, want %q", tc.cpu, tc.memMB, got, tc.want)
		}
	}
}
