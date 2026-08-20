// Package runtime is the JavaScript a station is written in, running inside
// the worker process and nowhere else.
//
// goja is the interpreter: pure Go, no CGO, which is the constraint that
// picked modernc's SQLite for the same project. What recommends it here is
// that it gives a program nothing at all — no fetch, no require, no import, no
// filesystem, no timers, no access to the host process — so the only things a
// station can touch are the ones injected by hand in bridge.go.
//
// It is the first of two layers and not the whole answer. An interpreter is
// not a security boundary; the process around it is, and everything privileged
// in here is a request sent up a pipe to a parent that holds the privileges
// and checks the permissions. If goja were broken open tomorrow, what an
// attacker would be standing in is a process with no socket, no disk and no
// network.
//
// Everything is synchronous. There is no event loop, no promises, no
// setTimeout: a station action is a function that runs and returns, which is
// both simpler to write and simpler to bound.
package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"quasar/internal/station/worker"

	"github.com/dop251/goja"
)

// scriptName is what appears in a stack trace. The author wrote one file, so
// it is called one thing.
const scriptName = "station.js"

// Run is the worker's engine: load the script, call one of its exported
// functions, hand back what it returned.
func Run(call worker.Call, req worker.Requester) (json.RawMessage, error) {
	vm := goja.New()

	// The budget, enforced from inside so that an ordinary runaway loop comes
	// back as a timeout its author can read rather than as a process that was
	// shot. The parent kills a worker that ignores this; the point of having
	// both is that the legible failure is the one that usually happens.
	if call.WallMS > 0 {
		budget := time.AfterFunc(time.Duration(call.WallMS)*time.Millisecond, func() {
			vm.Interrupt(errTimeout)
		})
		defer budget.Stop()
	}

	if err := installBridge(vm, call, req); err != nil {
		return nil, err
	}

	if _, err := vm.RunScript(scriptName, loadable(call.Script)); err != nil {
		return nil, readable(err, "the script did not load")
	}

	fn, err := action(vm, call.Action)
	if err != nil {
		return nil, err
	}

	input, err := toValue(vm, call.Input)
	if err != nil {
		return nil, err
	}

	value, err := fn(goja.Undefined(), input)
	if err != nil {
		return nil, readable(err, fmt.Sprintf("%s failed", call.Action))
	}
	return marshal(value)
}

// errTimeout is what the interrupt carries, so the message the author reads is
// about their loop rather than about goja.
var errTimeout = errors.New("this took longer than it is allowed to and was stopped")

// exportRe matches the one form an action may be declared in. Keeping it to a
// single form is deliberate: a station is meant to be read before it is
// trusted, and the validation, this loader and the documentation all agree on
// where its entry points are.
var exportRe = regexp.MustCompile(`(?m)^([ \t]*)export([ \t]+function[ \t])`)

// loadable turns the document's script into something goja can run.
//
// goja has no module system, so `export` has to go. It is replaced with spaces
// rather than deleted so that every character after it stays where it was: a
// stack trace pointing at the wrong column is a small thing until it is the
// only thing you have.
func loadable(script string) string {
	return exportRe.ReplaceAllString(script, "$1      $2")
}

// action finds the function the call named, and says so plainly when it is not
// there. An author reading "undefined is not a function" has been told nothing.
func action(vm *goja.Runtime, name string) (goja.Callable, error) {
	if name == "" {
		return nil, errors.New("no action was named")
	}
	v := vm.Get(name)
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return nil, fmt.Errorf("this script exports no action called %q", name)
	}
	fn, ok := goja.AssertFunction(v)
	if !ok {
		return nil, fmt.Errorf("%q is not a function", name)
	}
	return fn, nil
}

// toValue turns the input JSON into something the script can read.
func toValue(vm *goja.Runtime, raw json.RawMessage) (goja.Value, error) {
	if len(raw) == 0 {
		return vm.ToValue(map[string]any{}), nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("the input for this action is not readable: %w", err)
	}
	return vm.ToValue(v), nil
}

// marshal turns what the action returned into what goes back up the pipe.
func marshal(v goja.Value) (json.RawMessage, error) {
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return json.RawMessage("null"), nil
	}
	out, err := json.Marshal(v.Export())
	if err != nil {
		// A function, a cycle, something with no JSON in it. Said in the terms
		// the author was working in rather than Go's.
		return nil, fmt.Errorf("this action returned something that cannot be sent back: %s", plainly(err))
	}
	return out, nil
}

// plainly strips Go's own vocabulary out of a marshalling failure.
func plainly(err error) string {
	msg := strings.TrimPrefix(err.Error(), "json: ")
	msg = strings.TrimPrefix(msg, "unsupported value: ")
	return strings.TrimPrefix(msg, "unsupported type: ")
}

// readable turns a goja failure into something worth showing.
//
// An interrupt is the budget running out and says so. A thrown value is the
// author's own error and keeps its message and its stack. Anything else is a
// syntax error or the like, which goja already reports well.
func readable(err error, context string) error {
	var interrupted *goja.InterruptedError
	if errors.As(err, &interrupted) {
		return errTimeout
	}
	var thrown *goja.Exception
	if errors.As(err, &thrown) {
		return errors.New(strings.TrimSpace(thrown.String()))
	}
	return fmt.Errorf("%s: %w", context, err)
}
