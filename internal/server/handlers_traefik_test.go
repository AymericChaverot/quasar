package server

import (
	"errors"
	"testing"

	"quasar/internal/version"
)

// Two operators clicking the same button must not start two updates of the same
// router — the second would recreate a container the first is still bringing up.
func TestTraefikRunAdmitsOneRunAtATime(t *testing.T) {
	var run traefikRun

	if !run.begin("traefik:v3.7.10") {
		t.Fatal("the first run should have been admitted")
	}
	if run.begin("traefik:v3.7.10") {
		t.Error("a second run was admitted while the first was pulling")
	}
	run.recreating()
	if run.begin("traefik:v3.7.10") {
		t.Error("a second run was admitted while the router was being recreated")
	}

	// A finished run is over either way, and the operator must be able to try
	// again — after a failure especially — without restarting the dashboard.
	run.finish(nil)
	if !run.begin("traefik:v3.7.11") {
		t.Fatal("a run after a finished one was refused")
	}
	if got := run.state(); got.target != "traefik:v3.7.11" {
		t.Errorf("the new run kept the old one's target: %q", got.target)
	}
}

// An update that worked leaves nothing behind to report. The row names the
// version the router is on, which is the whole of the good news; a success
// banner would otherwise sit under it on every visit to the page, for as long
// as the dashboard stays up.
func TestTraefikRunKeepsNothingAfterSuccess(t *testing.T) {
	var run traefikRun
	run.begin("traefik:v3.7.10")
	run.progress(80, "Extracting")
	run.recreating()
	run.finish(nil)

	got := run.state()
	if got.phase != "" || got.err != "" || got.target != "" || got.percent != 0 || got.detail != "" {
		t.Errorf("state after a successful run = %q/%q/%q/%v/%q, want nothing left to report",
			got.phase, got.err, got.target, got.percent, got.detail)
	}
}

// A failure is kept — it is the only place the reason is written down — but
// only while it still describes the server. Once the router is on the tested
// version, however it got there, the old failure is history.
func TestTraefikViewDropsAFailureThatIsNoLongerTrue(t *testing.T) {
	s := &Server{}
	s.traefik.begin(version.TraefikImage)
	s.traefik.finish(errors.New("rolled back"))

	if v := s.traefikView("traefik:v3.0.0", true); v.Phase != traefikFailed || v.Err == "" {
		t.Errorf("a router still on the old version = %+v, want the failure kept", v)
	}
	if v := s.traefikView(version.TraefikImage, true); v.Phase != "" || v.Err != "" {
		t.Errorf("a router now on the tested version = %+v, want the stale failure dropped", v)
	}
}

func TestTraefikRunKeepsTheFailure(t *testing.T) {
	var run traefikRun
	run.begin("traefik:v3.7.10")
	run.finish(errors.New("rolled back"))

	got := run.state()
	if got.phase != traefikFailed || got.err != "rolled back" {
		t.Errorf("state = %q/%q, want %q with the error kept", got.phase, got.err, traefikFailed)
	}
	// The row stops polling once there is an outcome; only a run still going
	// changes on its own.
	if (TraefikView{Phase: got.phase}).Busy() {
		t.Error("a failed run still reports itself as busy, so the card would poll forever")
	}
}

// An update is offered only when the tested version is ahead of the running
// one. Offering a downgrade would second-guess an operator who deliberately
// runs a newer Traefik, and offering an update to the version already running
// would be an invitation to restart every site for nothing.
func TestTraefikViewOffersOnlyForwardUpdates(t *testing.T) {
	s := &Server{}
	tested := version.TraefikImage

	for _, tc := range []struct {
		name    string
		running string
		want    bool
	}{
		{"an older router", "traefik:v3.0.0", true},
		{"the tested version", tested, false},
		{"a newer router the operator chose", "traefik:v99.0.0", false},
		// The daemon could not be asked, so there is nothing to compare with.
		{"an unknown router", "", false},
	} {
		if got := s.traefikView(tc.running, true).Available; got != tc.want {
			t.Errorf("%s (%q vs tested %q): available = %v, want %v", tc.name, tc.running, tested, got, tc.want)
		}
	}
}

// A run in flight is reported whatever the versions say, so the card can show
// what is happening rather than going quiet mid-update.
func TestTraefikViewCarriesTheRunInFlight(t *testing.T) {
	s := &Server{}
	s.traefik.begin(version.TraefikImage)
	s.traefik.progress(42, "Downloading")

	v := s.traefikView("traefik:v3.0.0", true)
	if !v.Busy() || v.Percent != 42 || v.Detail != "Downloading" {
		t.Errorf("view = %+v, want a busy pull at 42%% Downloading", v)
	}
}

func TestImageTag(t *testing.T) {
	cases := map[string]string{
		"traefik:v3.7.10":             "v3.7.10",
		"ghcr.io/owner/quasar:v1.2.3": "v1.2.3",
		// No tag at all, and a registry whose port is not a tag separator.
		"traefik":                       "",
		"registry:5000/traefik:v3.7.10": "v3.7.10",
		"registry:5000/traefik":         "",
	}
	for ref, want := range cases {
		if got := imageTag(ref); got != want {
			t.Errorf("imageTag(%q) = %q, want %q", ref, got, want)
		}
	}
}
