package docker

import (
	"fmt"
	"sync"
)

// A deploy is watched from a page that was open before it started and is still
// open after it ends, so what it prints is kept here rather than pushed at
// whoever happens to be connected at the time: a reader arriving in the middle
// of a build sees it from its first line, and one arriving after it failed
// still gets to read why.

// deployLogLines caps how much of one deploy's output is kept. A build that
// prints a line per file copied must not grow the dashboard without bound, and
// past that many lines the beginning is no longer what anyone is looking for.
const deployLogLines = 2000

// The steps a deploy moves through. Which of them a given deploy actually runs
// depends on how the app is built, which is what deployPlanFor decides.
const (
	phaseFetch  = "fetch"  // pull an image, clone or advance a checkout
	phaseBuild  = "build"  // build an image or a stack from source
	phaseStart  = "start"  // create and start the containers
	phaseHealth = "health" // wait for the new container to serve
)

// deployPhase is one step of a deploy: what to call it while it runs, and what
// share of the progress bar it is worth. The weights are relative and only have
// to be right about which steps take longer than which — a bar moving in equal
// jumps through steps of wildly unequal length reads as broken.
type deployPhase struct {
	name   string
	label  string
	weight float64
}

// DeployLine is one line of a deploy's output. Note marks Quasar's own
// commentary — "pulling nginx:latest", "waiting for it to serve" — as opposed
// to what git, the builder or compose printed, so a reader can tell the two
// apart in a pane where they are interleaved.
type DeployLine struct {
	Seq  int
	Note bool
	Text string
}

// DeploySnapshot is everything a watcher needs for one update: the lines it has
// not seen yet, and where the deploy has got to.
type DeploySnapshot struct {
	Gen     int64 // which deploy this is; a change means a new one started
	Seq     int   // sequence of the last line included, -1 when there is none
	Reset   bool  // the watcher's position is gone: redraw from Lines
	Running bool
	Err     string
	Phase   string  // label of the step being run, "" when idle
	Percent float64 // 0 to 100 across the whole deploy
	// Measured reports whether Percent is being fed by something that actually
	// knows how far it has got. A clone and a stack build cannot say, and a bar
	// that has stopped moving is better shown as work than as a stalled number.
	Measured bool
	Lines    []DeployLine
}

// deployRun is the live account of one app's deploy: what it has printed, how
// far it has got, and the channel watchers wait on for the next change.
type deployRun struct {
	mu      sync.Mutex
	gen     int64 // 0 until the first deploy of this boot
	running bool
	err     string

	plan     []deployPhase
	step     int // index into plan; len(plan) once it is done
	frac     float64
	measured bool

	lines []DeployLine
	first int // sequence of lines[0]

	// changed is closed and replaced whenever anything above moves, which is how
	// watchers wait without polling and without this having to know they exist.
	changed chan struct{}
}

func newDeployRun() *deployRun {
	return &deployRun{changed: make(chan struct{})}
}

// touch wakes every watcher. Called with the lock held.
func (r *deployRun) touch() {
	close(r.changed)
	r.changed = make(chan struct{})
}

// start begins a new deploy, discarding the previous one's output: the pane is
// about to be redrawn from scratch, and keeping two builds' lines in one buffer
// would only make the current one harder to read.
func (r *deployRun) start(plan []deployPhase) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gen++
	r.running, r.err = true, ""
	r.plan, r.step, r.frac, r.measured = plan, 0, 0, false
	r.lines, r.first = nil, 0
	r.touch()
}

func (r *deployRun) add(note bool, text string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, DeployLine{Seq: r.first + len(r.lines), Note: note, Text: text})
	if drop := len(r.lines) - deployLogLines; drop > 0 {
		r.lines = append(r.lines[:0], r.lines[drop:]...)
		r.first += drop
	}
	r.touch()
}

// stage moves the deploy on to a named step. The step it is already on, and any
// step behind it, are ignored: one step can be reached by more than one path —
// deployImage runs both as a plain image deploy and as the tail of a git build,
// and compose announces its start over and over as each container comes up — and
// a bar that went backwards, or that dropped what it had measured every time it
// was told again where it was, would be reporting something that did not happen.
func (r *deployRun) stage(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.plan {
		if r.plan[i].name != name {
			continue
		}
		if i > r.step {
			r.step, r.frac, r.measured = i, 0, false
			r.touch()
		}
		return
	}
}

// progress reports how far through the current step the deploy is, for the
// steps that can tell. It only ever moves forward within a step.
func (r *deployRun) progress(frac float64) {
	if frac < 0 {
		frac = 0
	} else if frac > 1 {
		frac = 1
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.measured && frac <= r.frac {
		return
	}
	r.frac, r.measured = frac, true
	r.touch()
}

// finish closes the deploy out. A success fills the bar; a failure leaves it
// where it stopped, which is itself the answer to "how far did it get".
func (r *deployRun) finish(errMsg string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.running, r.err = false, errMsg
	if errMsg == "" {
		r.step, r.frac, r.measured = len(r.plan), 0, true
	}
	r.touch()
}

// percentLocked turns "which step, how far into it" into the one number the bar
// is drawn from.
func (r *deployRun) percentLocked() float64 {
	total := 0.0
	for _, p := range r.plan {
		total += p.weight
	}
	if total <= 0 {
		return 0
	}
	done := 0.0
	for i, p := range r.plan {
		switch {
		case i < r.step:
			done += p.weight
		case i == r.step:
			done += r.frac * p.weight
		}
	}
	return done / total * 100
}

func (r *deployRun) labelLocked() string {
	if r.step < len(r.plan) {
		return r.plan[r.step].label
	}
	return ""
}

// snapshot reports the deploy past the given position, together with the
// channel that closes when there is more. Both come from under one lock, so a
// watcher cannot miss a change between reading the state and waiting on it.
func (r *deployRun) snapshot(gen int64, seq int) (DeploySnapshot, <-chan struct{}) {
	r.mu.Lock()
	defer r.mu.Unlock()

	snap := DeploySnapshot{
		Gen:      r.gen,
		Seq:      r.first + len(r.lines) - 1,
		Running:  r.running,
		Err:      r.err,
		Phase:    r.labelLocked(),
		Percent:  r.percentLocked(),
		Measured: r.measured,
	}
	from := 0
	// A watcher on another deploy, or one whose next line has already been
	// evicted, has nothing to append to and is sent the buffer as it stands.
	if gen != r.gen || seq+1 < r.first {
		snap.Reset = true
	} else if from = seq + 1 - r.first; from > len(r.lines) {
		from = len(r.lines)
	}
	snap.Lines = append([]DeployLine(nil), r.lines[from:]...)
	return snap, r.changed
}

// deployRunFor returns the app's deploy record, creating an empty one the first
// time anything asks for it. The map is made here as well as in New, so that a
// Client assembled field by field — which the tests do, to reach the deploy
// path without a daemon — records its output like any other.
func (c *Client) deployRunFor(appID string) *deployRun {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.runs == nil {
		c.runs = map[string]*deployRun{}
	}
	r := c.runs[appID]
	if r == nil {
		r = newDeployRun()
		c.runs[appID] = r
	}
	return r
}

// WatchDeploy reports an app's deploy past the given position, and the channel
// that closes when it moves. gen -1 and seq -1 ask for everything there is.
func (c *Client) WatchDeploy(appID string, gen int64, seq int) (DeploySnapshot, <-chan struct{}) {
	return c.deployRunFor(appID).snapshot(gen, seq)
}

// note records Quasar's own commentary about what a deploy is doing.
func (c *Client) note(appID, format string, args ...any) {
	c.deployRunFor(appID).add(true, redactURLs(fmt.Sprintf(format, args...)))
}

// output records a line git, the image builder or compose printed. It is
// redacted like everything else here: a repository's own submodule URL, or a
// clone URL an operator pasted credentials into, reaches the browser through
// this.
func (c *Client) output(appID, line string) {
	c.deployRunFor(appID).add(false, redactURLs(line))
}

func (c *Client) stage(appID, name string)         { c.deployRunFor(appID).stage(name) }
func (c *Client) progress(appID string, f float64) { c.deployRunFor(appID).progress(f) }
