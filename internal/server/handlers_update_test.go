package server

import (
	"testing"
	"time"
)

// Two tabs, two clicks on the same button: the second must join the run in
// progress rather than start a second pull of the same image over the same
// link — and, worse, a second updater container racing the first.
func TestUpdateRunAdmitsOneRunAtATime(t *testing.T) {
	var run updateRun

	if !run.begin("v1.1.0") {
		t.Fatal("the first run should have been admitted")
	}
	if run.begin("v1.1.0") {
		t.Error("a second run was admitted while the first was pulling")
	}
	run.handoff()
	if run.begin("v1.1.0") {
		t.Error("a second run was admitted while the first was handing off")
	}

	// A run that failed is over, and the operator must be able to try again
	// without restarting the dashboard.
	run.fail("pull: unauthorized")
	if got := run.state(); got.phase != updateFailed || got.err != "pull: unauthorized" {
		t.Fatalf("state after a failure = %q/%q, want %q with the error kept", got.phase, got.err, updateFailed)
	}
	if !run.begin("v1.2.0") {
		t.Error("a run after a failed one was refused")
	}
	if got := run.state(); got.target != "v1.2.0" || got.err != "" {
		t.Errorf("the new run kept the old one's target or error: %q/%q", got.target, got.err)
	}
}

func TestUpdateRunReportsProgress(t *testing.T) {
	var run updateRun
	run.begin("v1.1.0")
	run.progress(42.5, "Downloading")

	got := run.state()
	if got.phase != updatePulling || got.percent != 42.5 || got.detail != "Downloading" {
		t.Errorf("state = %q %v%% %q, want pulling 42.5%% Downloading", got.phase, got.percent, got.detail)
	}
	// The transfer is done by definition once the updater container is started,
	// and the bar must not be left short of the end for the rest of the wait.
	run.handoff()
	if got := run.state(); got.percent != 100 {
		t.Errorf("percent after handoff = %v, want 100", got.percent)
	}
}

func TestHumanCheckedAt(t *testing.T) {
	now := time.Now()
	cases := map[string]string{
		now.Add(-10 * time.Second).Format(time.RFC3339): "just now",
		now.Add(-5 * time.Minute).Format(time.RFC3339):  "5 min ago",
		now.Add(-3 * time.Hour).Format(time.RFC3339):    "3h ago",
		// Never checked, or a value written by something else: better shown as
		// nothing than as a date that was never a check.
		"":           "",
		"not a time": "",
		"2026-07-22": "",
	}
	for stored, want := range cases {
		if got := humanCheckedAt(stored); got != want {
			t.Errorf("humanCheckedAt(%q) = %q, want %q", stored, got, want)
		}
	}
	old := now.Add(-72 * time.Hour)
	if got, want := humanCheckedAt(old.Format(time.RFC3339)), old.Format("2006-01-02"); got != want {
		t.Errorf("a check from days ago = %q, want the date %q", got, want)
	}
}
