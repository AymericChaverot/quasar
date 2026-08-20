package worker

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// The tests spawn this test binary as the worker, which is the same
// arrangement the dashboard uses on itself: one binary, re-executed in a mode
// that reads a call and answers it. Spawning something else would be testing a
// different thing from the one that ships.
const helperEnv = "QUASAR_WORKER_TEST_MODE"

// TestWorkerHelperProcess is the worker the tests below start. It skips when
// run as an ordinary test, and never returns when it is not.
func TestWorkerHelperProcess(t *testing.T) {
	mode := os.Getenv(helperEnv)
	if mode == "" {
		t.Skip("this test is the worker the others spawn")
	}
	_ = Serve(os.Stdin, os.Stdout, helperEngine(mode))
	// Before the testing package can print anything of its own onto the pipe.
	os.Exit(0)
}

// helperEngine is the runtime, for one call, in whichever of the ways worth
// testing it can behave.
func helperEngine(mode string) Engine {
	return func(call Call, req Requester) (json.RawMessage, error) {
		switch mode {
		case "echo":
			return call.Input, nil

		case "throw":
			return nil, errors.New("the script threw: no such mod")

		case "capability":
			// Everything privileged goes back up the pipe. Whether it comes
			// back with a value or a refusal is the parent's business.
			return req.Request("files.read", map[string]string{"path": "data/x"})

		case "huge":
			return json.RawMessage(`"` + strings.Repeat("x", 4<<20) + `"`), nil

		case "spin":
			// A worker that ignores every budget it was given.
			for {
			}

		case "grow":
			// Allocation without pause, bounded only so that a broken kill
			// does not take the machine with it.
			var held [][]byte
			for i := 0; i < 400; i++ {
				chunk := make([]byte, 4<<20)
				for j := range chunk {
					chunk[j] = byte(j)
				}
				held = append(held, chunk)
				time.Sleep(2 * time.Millisecond)
			}
			return json.RawMessage(`"survived"`), nil

		case "crash":
			os.Exit(3)
		}
		return nil, errors.New("unknown helper mode " + mode)
	}
}

// helper is a Spawner that starts this test binary as a worker in one mode.
func helper(mode string) Spawner {
	return Spawner{
		Argv: []string{os.Args[0], "-test.run=^TestWorkerHelperProcess$"},
		Env:  []string{helperEnv + "=" + mode},
	}
}

// testLimits are the shipped ones, shortened so a test that has to wait for a
// budget to run out waits for a fraction of a second rather than ten.
func testLimits() Limits {
	l := DefaultLimits()
	l.Wall = 300 * time.Millisecond
	l.Grace = 200 * time.Millisecond
	return l
}

func run(t *testing.T, mode string, lim Limits, b Broker) (Outcome, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return Run(ctx, helper(mode), Call{Action: "go", Input: json.RawMessage(`{"n":1}`)}, lim, b)
}

func TestAWorkerRunsOneCallAndAnswers(t *testing.T) {
	out, err := run(t, "echo", testLimits(), DenyAll())
	if err != nil {
		t.Fatalf("the call did not come back: %v", err)
	}
	if string(out.Value) != `{"n":1}` {
		t.Errorf("got %s, want the input back", out.Value)
	}
}

// A script that throws is the author's bug and reads like one: the message
// reaches the panel, and nothing about it looks like Quasar falling over.
func TestAScriptErrorComesBackAsItself(t *testing.T) {
	_, err := run(t, "throw", testLimits(), DenyAll())
	var se *ScriptError
	if !errors.As(err, &se) {
		t.Fatalf("error is %T (%v), want the script's own", err, err)
	}
	if !strings.Contains(se.Message, "no such mod") {
		t.Errorf("the message did not survive: %q", se.Message)
	}
}

// The point of building the boundary before the runtime: a capability crosses
// a process boundary the parent polices, so refusing one is a decision made
// where the privileges are, not a binding somebody remembered not to inject.
func TestACapabilityIsRefusedWhenNothingWasGranted(t *testing.T) {
	_, err := run(t, "capability", testLimits(), DenyAll())
	if err == nil {
		t.Fatal("an ungranted capability was performed")
	}
	if !strings.Contains(err.Error(), "files.read") {
		t.Errorf("the refusal does not name the capability: %v", err)
	}
}

// And one the station did declare comes back with what the parent did on its
// behalf, which the worker could not have done for itself.
func TestACapabilityTheBrokerPerformsComesBack(t *testing.T) {
	var asked string
	b := BrokerFunc(func(_ context.Context, capability string, args json.RawMessage) (json.RawMessage, error) {
		asked = capability
		return json.RawMessage(`"the file's contents"`), nil
	})

	out, err := run(t, "capability", testLimits(), b)
	if err != nil {
		t.Fatalf("the call did not come back: %v", err)
	}
	if asked != "files.read" {
		t.Errorf("the parent was asked for %q", asked)
	}
	if string(out.Value) != `"the file's contents"` {
		t.Errorf("got %s, want what the parent handed back", out.Value)
	}
}

// A worker that ignores its own interrupt is killed from outside, and the
// panel is told which limit it hit rather than that something went wrong.
func TestAWorkerThatIgnoresItsBudgetIsKilled(t *testing.T) {
	started := time.Now()
	_, err := run(t, "spin", testLimits(), DenyAll())

	var f *Failure
	if !errors.As(err, &f) || f.Reason != FailTimeout {
		t.Fatalf("error is %v, want a timeout", err)
	}
	if !strings.Contains(f.Error(), "longer than it is allowed") {
		t.Errorf("the message does not say what happened: %q", f)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Errorf("it took %s to stop a runaway worker", elapsed)
	}
}

// Memory is bounded from outside the process being bounded. A watchdog living
// inside it would only notice after the kernel had picked a victim, and on a
// server the victim it picks may be the dashboard.
func TestAWorkerThatAllocatesWithoutPauseIsKilled(t *testing.T) {
	lim := testLimits()
	lim.Wall, lim.Grace = 20*time.Second, time.Second // out of the way
	lim.MaxMemoryBytes = 150 << 20

	_, err := run(t, "grow", lim, DenyAll())
	var f *Failure
	if !errors.As(err, &f) || f.Reason != FailMemory {
		t.Fatalf("error is %v, want the memory ceiling", err)
	}
	if !strings.Contains(f.Error(), "memory") {
		t.Errorf("the message does not name the limit: %q", f)
	}
}

// A worker that dies is an error on a panel. The dashboard is the one process
// on the machine that must not go with it.
func TestAWorkerThatCrashesIsAnErrorAndNothingMore(t *testing.T) {
	_, err := run(t, "crash", testLimits(), DenyAll())
	var f *Failure
	if !errors.As(err, &f) || f.Reason != FailCrash {
		t.Fatalf("error is %v, want a crash", err)
	}
	if !strings.Contains(f.Detail, "3") {
		t.Errorf("the report does not say how it died: %q", f.Detail)
	}

	// And the boundary still works afterwards, which is the whole claim.
	if _, err := run(t, "echo", testLimits(), DenyAll()); err != nil {
		t.Errorf("the next call failed too: %v", err)
	}
}

// The cap on a returned value is the parent's. A worker that has been taken
// over is exactly the one that would ignore its own.
func TestAResultOverTheCapIsRefused(t *testing.T) {
	lim := testLimits()
	lim.Wall = 5 * time.Second
	lim.MaxResultBytes = 64 << 10

	_, err := run(t, "huge", lim, DenyAll())
	if err == nil {
		t.Fatal("a result over the cap came through")
	}
	// Either end may be the one that says so; what matters is that it did.
	if !strings.Contains(err.Error(), "over the") && !strings.Contains(err.Error(), "more data") {
		t.Errorf("the refusal does not say why: %v", err)
	}
}

// The worker is given nothing of the dashboard's environment. Everything in it
// is something a station has no business reading.
func TestTheWorkerGetsAnEmptyEnvironment(t *testing.T) {
	sp, err := Self()
	if err != nil {
		t.Fatal(err)
	}
	if len(sp.Env) != 0 {
		t.Errorf("the worker would inherit %v", sp.Env)
	}
	if len(sp.Argv) != 2 || !strings.HasSuffix(sp.Argv[1], "station-worker") {
		t.Errorf("argv = %v, want this binary in worker mode", sp.Argv)
	}
}
