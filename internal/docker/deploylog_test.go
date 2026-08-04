package docker

import (
	"fmt"
	"testing"
)

func TestDeployRunSendsEverythingToANewWatcher(t *testing.T) {
	r := newDeployRun()
	r.start(imagePlan)
	r.add(true, "pulling nginx")
	r.add(false, "Status: Downloaded")

	// -1/-1 is what a freshly opened page asks with: it has nothing on screen,
	// so it has to be told to draw from scratch.
	snap, _ := r.snapshot(-1, -1)
	if !snap.Reset {
		t.Error("a watcher that has seen nothing must be told to redraw")
	}
	if len(snap.Lines) != 2 {
		t.Fatalf("got %d lines, want both", len(snap.Lines))
	}
	if snap.Seq != 1 {
		t.Errorf("Seq = %d, want 1", snap.Seq)
	}
}

func TestDeployRunSendsOnlyWhatIsNew(t *testing.T) {
	r := newDeployRun()
	r.start(imagePlan)
	r.add(false, "first")
	snap, _ := r.snapshot(-1, -1)

	r.add(false, "second")
	snap, _ = r.snapshot(snap.Gen, snap.Seq)
	if snap.Reset {
		t.Error("a watcher that is up to date must not be made to redraw")
	}
	if len(snap.Lines) != 1 || snap.Lines[0].Text != "second" {
		t.Errorf("got %v, want only the line that arrived since", snap.Lines)
	}

	// Nothing has happened since, so there is nothing to send.
	snap, _ = r.snapshot(snap.Gen, snap.Seq)
	if len(snap.Lines) != 0 {
		t.Errorf("got %v, want nothing", snap.Lines)
	}
}

// A new deploy replaces the pane rather than appending to it, or the reader
// would be looking at two builds run together as one.
func TestDeployRunResetsWatchersOnTheNextDeploy(t *testing.T) {
	r := newDeployRun()
	r.start(imagePlan)
	r.add(false, "old build")
	snap, _ := r.snapshot(-1, -1)

	r.start(imagePlan)
	r.add(false, "new build")
	snap, _ = r.snapshot(snap.Gen, snap.Seq)
	if !snap.Reset {
		t.Fatal("a watcher left on the previous deploy must be told to redraw")
	}
	if len(snap.Lines) != 1 || snap.Lines[0].Text != "new build" {
		t.Errorf("got %v, want the new deploy's lines alone", snap.Lines)
	}
}

// A build long enough to overflow the buffer leaves a watcher pointing at a
// line that is gone. It has to be told, or it would silently skip the gap.
func TestDeployRunResetsWhenLinesHaveBeenEvicted(t *testing.T) {
	r := newDeployRun()
	r.start(imagePlan)
	r.add(false, "the first line")
	snap, _ := r.snapshot(-1, -1)

	for i := 0; i < deployLogLines+10; i++ {
		r.add(false, fmt.Sprintf("line %d", i))
	}
	if got := len(r.lines); got != deployLogLines {
		t.Fatalf("kept %d lines, want the buffer capped at %d", got, deployLogLines)
	}

	snap, _ = r.snapshot(snap.Gen, snap.Seq)
	if !snap.Reset {
		t.Error("a watcher whose next line was evicted must be told to redraw")
	}
	if len(snap.Lines) != deployLogLines {
		t.Errorf("got %d lines, want the whole buffer", len(snap.Lines))
	}
}

// deployImage runs as the tail of a git build as well as on its own, and
// compose announces that it has started containers once per container. Either
// would drag the bar backwards if a step could be re-entered.
func TestDeployRunStageOnlyMovesForward(t *testing.T) {
	r := newDeployRun()
	r.start(gitImagePlan)

	r.stage(phaseBuild)
	r.progress(0.5)
	before := r.snap()

	r.stage(phaseFetch) // behind it
	if got := r.snap(); got.Percent != before.Percent {
		t.Errorf("percent moved to %.1f on a step already passed, want %.1f", got.Percent, before.Percent)
	}

	r.stage(phaseBuild) // the one it is already on
	if got := r.snap(); got.Percent != before.Percent || !got.Measured {
		t.Errorf("being told again where it is dropped what it had measured: %+v", got)
	}
}

func TestDeployRunPercentFollowsThePlan(t *testing.T) {
	plan := []deployPhase{{phaseFetch, "Fetching", 25}, {phaseBuild, "Building", 75}}
	r := newDeployRun()
	r.start(plan)

	if got := r.snap(); got.Percent != 0 || got.Measured {
		t.Errorf("a deploy that has not reported anything is at %+v, want nothing measured", got)
	}
	r.progress(0.5)
	if got := r.snap().Percent; got != 12.5 {
		t.Errorf("half of a quarter-weight step = %.2f%%, want 12.5%%", got)
	}
	r.stage(phaseBuild)
	if got := r.snap().Percent; got != 25 {
		t.Errorf("the whole first step = %.2f%%, want 25%%", got)
	}
	if got := r.snap().Phase; got != "Building" {
		t.Errorf("Phase = %q, want the label of the step being run", got)
	}

	r.finish("")
	if got := r.snap(); got.Percent != 100 || got.Running {
		t.Errorf("a finished deploy is %+v, want a full bar and nothing running", got)
	}
}

// A failure leaves the bar where it stopped: how far it got is part of the
// answer, and filling it would say the deploy had finished.
func TestDeployRunFailureKeepsThePosition(t *testing.T) {
	r := newDeployRun()
	r.start(imagePlan)
	r.stage(phaseStart)
	before := r.snap().Percent

	r.finish("new container exited on startup")
	got := r.snap()
	if got.Percent != before {
		t.Errorf("percent = %.1f after a failure, want it left at %.1f", got.Percent, before)
	}
	if got.Running || got.Err == "" {
		t.Errorf("got %+v, want a stopped deploy carrying its error", got)
	}
}

// Watchers block on the channel the snapshot hands back rather than polling,
// so anything that moves has to close it.
func TestDeployRunWakesItsWatchers(t *testing.T) {
	r := newDeployRun()
	r.start(imagePlan)
	_, changed := r.snapshot(-1, -1)

	select {
	case <-changed:
		t.Fatal("woken before anything happened")
	default:
	}

	r.add(false, "something happened")
	select {
	case <-changed:
	default:
		t.Error("a line arrived and no watcher was woken")
	}
}

// snap is the current state, for tests that do not care about the lines.
func (r *deployRun) snap() DeploySnapshot {
	s, _ := r.snapshot(r.gen, r.first+len(r.lines)-1)
	return s
}
