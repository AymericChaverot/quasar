package runtime_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"quasar/internal/station/runtime"
	"quasar/internal/station/worker"
)

// These run the real thing: the test binary re-executed as a worker, serving
// with the same engine the dashboard ships. Testing the runtime in-process
// would leave out the half that makes it safe.
const helperEnv = "QUASAR_RUNTIME_TEST_WORKER"

func TestRuntimeHelperProcess(t *testing.T) {
	if os.Getenv(helperEnv) == "" {
		t.Skip("this test is the worker the others spawn")
	}
	_ = worker.Serve(os.Stdin, os.Stdout, runtime.Run)
	os.Exit(0)
}

func helper() worker.Spawner {
	return worker.Spawner{
		Argv: []string{os.Args[0], "-test.run=^TestRuntimeHelperProcess$"},
		Env:  []string{helperEnv + "=1"},
	}
}

// asked records what the script reached for, so a test can show that a refused
// namespace never got as far as the parent.
type asked struct {
	capabilities []string
	answer       func(capability string, args json.RawMessage) (json.RawMessage, error)
}

func (a *asked) Do(_ context.Context, capability string, args json.RawMessage) (json.RawMessage, error) {
	a.capabilities = append(a.capabilities, capability)
	if a.answer == nil {
		return nil, errors.New("this station has not been granted " + capability)
	}
	return a.answer(capability, args)
}

// call runs one action of a script and hands back everything about the result.
func call(t *testing.T, script, action string, opts ...func(*worker.Call, *worker.Limits, *asked)) (worker.Outcome, error, *asked) {
	t.Helper()
	c := worker.Call{Script: script, Action: action, Input: json.RawMessage(`{}`)}
	lim := worker.DefaultLimits()
	lim.Wall, lim.Grace = 3*time.Second, time.Second
	broker := &asked{}
	for _, o := range opts {
		o(&c, &lim, broker)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := worker.Run(ctx, helper(), c, lim, broker)
	return out, err, broker
}

func TestAScriptReturnsAValue(t *testing.T) {
	out, err, _ := call(t, `
		export function player_count({ max }) {
			return { data: { value: 3, suffix: '/ ' + (max || 20) } }
		}
	`, "player_count", func(c *worker.Call, _ *worker.Limits, _ *asked) {
		c.Input = json.RawMessage(`{"max":50}`)
	})
	if err != nil {
		t.Fatalf("the call did not come back: %v", err)
	}
	var got struct {
		Data struct {
			Value  int    `json:"value"`
			Suffix string `json:"suffix"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out.Value, &got); err != nil {
		t.Fatalf("the result is not readable: %s", out.Value)
	}
	if got.Data.Value != 3 || got.Data.Suffix != "/ 50" {
		t.Errorf("got %+v, want the value and the input it was given", got.Data)
	}
}

// An ordinary runaway loop comes back as something its author can read. Being
// shot by the parent is the fallback, not the normal way this ends.
func TestARunawayLoopReportsATimeout(t *testing.T) {
	started := time.Now()
	_, err, _ := call(t, `export function spin() { while (true) {} }`, "spin",
		func(_ *worker.Call, lim *worker.Limits, _ *asked) {
			lim.Wall, lim.Grace = 400*time.Millisecond, 3*time.Second
		})

	var se *worker.ScriptError
	if !errors.As(err, &se) {
		t.Fatalf("error is %T (%v), want the script's own timeout", err, err)
	}
	if !strings.Contains(se.Message, "longer than it is allowed") {
		t.Errorf("the message does not say what happened: %q", se.Message)
	}
	// It reported rather than being killed, so it came back inside its own
	// budget rather than at the end of the grace period.
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Errorf("it took %s, which means the parent killed it instead", elapsed)
	}
}

// The value a call may answer with is bounded, and the bound is not a
// suggestion: a panel is a page, not a file transfer.
func TestAResultOverTheCapIsRefused(t *testing.T) {
	_, err, _ := call(t, `
		export function flood() { return { data: 'x'.repeat(10 * 1024 * 1024) } }
	`, "flood")
	if err == nil {
		t.Fatal("a ten megabyte result came through")
	}
	if !strings.Contains(err.Error(), "over the") && !strings.Contains(err.Error(), "more data") {
		t.Errorf("the refusal does not say why: %v", err)
	}
}

// The rule that makes a station debuggable: reaching for something the
// document did not declare says which line is missing.
func TestAnUngrantedNamespaceNamesThePermission(t *testing.T) {
	_, err, broker := call(t, `
		export function peek() { return { data: quasar.files.read('data/server.properties') } }
	`, "peek")

	if err == nil {
		t.Fatal("an ungranted namespace worked")
	}
	if !strings.Contains(err.Error(), `"files"`) || !strings.Contains(err.Error(), "quasar.files.read") {
		t.Errorf("the error does not name the permission or what wanted it: %v", err)
	}
	if strings.Contains(err.Error(), "undefined") {
		t.Errorf("the error reads as a missing object rather than a missing permission: %v", err)
	}
	// And it never got as far as the parent, so nothing had to be refused.
	if len(broker.capabilities) != 0 {
		t.Errorf("the parent was asked for %v", broker.capabilities)
	}
}

// The namespaces that need no permission are there, and the ones over the pipe
// go over the pipe.
func TestTheGrantedNamespacesWork(t *testing.T) {
	out, err, broker := call(t, `
		export function report() {
			quasar.log('checking', { mods: 2 })
			const seen = quasar.store.get('seen')
			quasar.store.set('seen', (seen || 0) + 1)
			return { data: { name: quasar.app.name, status: quasar.app.status, seen: seen } }
		}
	`, "report", func(c *worker.Call, _ *worker.Limits, b *asked) {
		c.App = json.RawMessage(`{"id":"abcd1234","name":"Minecraft 1.20.1","status":"running"}`)
		b.answer = func(capability string, args json.RawMessage) (json.RawMessage, error) {
			if capability == "store.get" {
				return json.RawMessage(`7`), nil
			}
			return json.RawMessage(`null`), nil
		}
	})
	if err != nil {
		t.Fatalf("the call did not come back: %v", err)
	}

	var got struct {
		Data struct {
			Name, Status string
			Seen         int
		} `json:"data"`
	}
	if err := json.Unmarshal(out.Value, &got); err != nil {
		t.Fatalf("the result is not readable: %s", out.Value)
	}
	if got.Data.Name != "Minecraft 1.20.1" || got.Data.Status != "running" {
		t.Errorf("quasar.app did not arrive: %+v", got.Data)
	}
	if got.Data.Seen != 7 {
		t.Errorf("quasar.store.get returned %d, want what the parent answered", got.Data.Seen)
	}
	if len(broker.capabilities) != 2 || broker.capabilities[0] != "store.get" || broker.capabilities[1] != "store.set" {
		t.Errorf("the parent was asked for %v", broker.capabilities)
	}
	if len(out.Logs) != 1 || !strings.Contains(out.Logs[0], `checking {"mods":2}`) {
		t.Errorf("the log line did not arrive: %v", out.Logs)
	}
}

// A script that throws is the author's bug, and the panel says so in their own
// words rather than in the interpreter's.
func TestAThrownErrorKeepsItsMessage(t *testing.T) {
	_, err, _ := call(t, `
		export function install() { throw new Error('no build of sodium for 1.20.1') }
	`, "install")
	if err == nil || !strings.Contains(err.Error(), "no build of sodium") {
		t.Errorf("error is %v, want the script's own message", err)
	}
}

// An action a panel names and the script does not export would otherwise fail
// as "undefined is not a function", which tells an author nothing.
func TestAMissingActionIsNamed(t *testing.T) {
	_, err, _ := call(t, `export function list_mods() { return { data: [] } }`, "list_mdos")
	if err == nil || !strings.Contains(err.Error(), "list_mdos") {
		t.Errorf("error is %v, want it to name the action", err)
	}
}

// A station gets one global. Everything an interpreter would normally hand a
// program — a way out to the network, the filesystem, the host process — is
// absent because it was never put there.
func TestTheScriptGetsNothingItWasNotGiven(t *testing.T) {
	for _, global := range []string{"fetch", "require", "process", "setTimeout", "XMLHttpRequest", "globalThis.Deno"} {
		out, err, _ := call(t, `
			export function look({ name }) { return { data: eval('typeof ' + name) } }
		`, "look", func(c *worker.Call, _ *worker.Limits, _ *asked) {
			c.Input = json.RawMessage(`{"name":"` + global + `"}`)
		})
		if err != nil {
			t.Fatalf("%s: %v", global, err)
		}
		var got struct {
			Data string `json:"data"`
		}
		json.Unmarshal(out.Value, &got)
		if got.Data != "undefined" {
			t.Errorf("%s is %q in a station's script", global, got.Data)
		}
	}
}
