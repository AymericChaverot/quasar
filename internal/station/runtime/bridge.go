package runtime

import (
	"encoding/json"
	"fmt"
	"strings"

	"quasar/internal/station/worker"

	"github.com/dop251/goja"
)

// The one global a station gets.
//
// It is populated by hand, namespace by namespace, because goja hands a
// program nothing and that is the property worth keeping: a station reaches
// exactly what is written here and there is no second door. Everything
// privileged is a request up the pipe — the worker cannot perform any of it —
// and the parent is what decides whether the station declared the permission,
// does the work, and writes the audit entry.
//
// A namespace that is not granted still exists. Calling into it throws an
// error naming the permission that is missing, because an author debugging an
// action that reads "undefined is not a function" learns nothing, and the
// thing they actually did wrong was forget a line in their document.

// bridge holds what the namespaces need: the VM to throw into and the pipe to
// ask down.
type bridge struct {
	vm  *goja.Runtime
	req worker.Requester
}

// installBridge builds the quasar global for one call.
func installBridge(vm *goja.Runtime, call worker.Call, req worker.Requester) error {
	b := &bridge{vm: vm, req: req}
	q := vm.NewObject()

	app, err := b.app(call.App)
	if err != nil {
		return err
	}
	if err := q.Set("app", app); err != nil {
		return err
	}

	// Needs no permission and reaches nothing: a line somebody may read.
	if err := q.Set("log", b.log); err != nil {
		return err
	}
	// Long actions stream this into a progress pane. Until they exist it is
	// the log, which is where the same sentence would have ended up anyway.
	if err := q.Set("progress", b.progress); err != nil {
		return err
	}

	// A small key–value space scoped to one application and one station. It
	// needs no permission because it can reach nothing else — but it does need
	// the parent, because a worker holds no disk and nothing in it survives
	// the call.
	if err := q.Set("store", b.store()); err != nil {
		return err
	}

	for name, ns := range b.ungranted() {
		if err := q.Set(name, ns); err != nil {
			return err
		}
	}
	return vm.Set("quasar", q)
}

// app is the application the call is about, with the verbs that act on it
// hung off it.
func (b *bridge) app(raw json.RawMessage) (*goja.Object, error) {
	fields := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &fields); err != nil {
			return nil, fmt.Errorf("the application this call is about is not readable: %w", err)
		}
	}
	obj := b.vm.ToValue(fields).(*goja.Object)
	for _, verb := range []string{"start", "stop", "restart", "redeploy", "setImage"} {
		if err := obj.Set(verb, b.refuse("lifecycle", "quasar.app."+verb)); err != nil {
			return nil, err
		}
	}
	return obj, nil
}

// log writes a line for whoever is reading the panel.
func (b *bridge) log(fc goja.FunctionCall) goja.Value {
	parts := make([]string, 0, len(fc.Arguments))
	for _, arg := range fc.Arguments {
		parts = append(parts, display(arg))
	}
	b.req.Log(strings.Join(parts, " "))
	return goja.Undefined()
}

// progress is what a long action reports itself with.
func (b *bridge) progress(pct float64, message string) goja.Value {
	b.req.Log(fmt.Sprintf("%.0f%% %s", pct, message))
	return goja.Undefined()
}

// store is the key–value space, over the pipe because that is where the disk
// is.
func (b *bridge) store() *goja.Object {
	obj := b.vm.NewObject()
	obj.Set("get", func(key string) goja.Value {
		return b.ask("store.get", map[string]any{"key": key})
	})
	obj.Set("set", func(key string, value goja.Value) goja.Value {
		return b.ask("store.set", map[string]any{"key": key, "value": exported(value)})
	})
	obj.Set("delete", func(key string) goja.Value {
		return b.ask("store.delete", map[string]any{"key": key})
	})
	obj.Set("keys", func() goja.Value {
		return b.ask("store.keys", map[string]any{})
	})
	return obj
}

// ungranted are the namespaces whose implementations live on the other side of
// the permission model. Each one exists so that reaching for it says which
// line is missing from the document rather than failing as an undefined.
func (b *bridge) ungranted() map[string]goja.Value {
	out := map[string]goja.Value{
		"exec":    b.vm.ToValue(b.refuse("exec", "quasar.exec")),
		"logs":    b.vm.ToValue(b.refuse("logs", "quasar.logs")),
		"service": b.vm.ToValue(b.refuse("net.internal", "quasar.service")),
		"notify":  b.vm.ToValue(b.refuse("notify", "quasar.notify")),
	}
	for name, spec := range map[string]struct {
		permission string
		methods    []string
	}{
		"files": {"files", []string{"list", "read", "readBytes", "write", "delete", "mkdir"}},
		"env":   {"env", []string{"get", "set"}},
		"http":  {"net.external", []string{"get", "post"}},
	} {
		obj := b.vm.NewObject()
		for _, m := range spec.methods {
			obj.Set(m, b.refuse(spec.permission, "quasar."+name+"."+m))
		}
		out[name] = obj
	}
	return out
}

// refuse is a function that throws, naming what the document would have to
// declare for it to work.
func (b *bridge) refuse(permission, what string) func(goja.FunctionCall) goja.Value {
	return func(goja.FunctionCall) goja.Value {
		panic(b.vm.NewGoError(fmt.Errorf(
			"%s needs the %q permission, which this station has not been granted", what, permission)))
	}
}

// ask sends one capability request and returns what came back, throwing into
// the script if the parent refused. A refusal is an ordinary JavaScript error:
// an action that wants to handle one can catch it.
func (b *bridge) ask(capability string, args any) goja.Value {
	raw, err := b.req.Request(capability, args)
	if err != nil {
		panic(b.vm.NewGoError(err))
	}
	if len(raw) == 0 {
		return goja.Undefined()
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		panic(b.vm.NewGoError(fmt.Errorf("%s answered with something unreadable: %w", capability, err)))
	}
	return b.vm.ToValue(v)
}

// exported turns a script value into something that survives JSON on its way
// to the parent.
func exported(v goja.Value) any {
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return nil
	}
	return v.Export()
}

// display is one logged argument as a line of text: strings as themselves,
// everything else as the JSON it is, which is what somebody logging an object
// wanted to see.
func display(v goja.Value) string {
	if v == nil || goja.IsUndefined(v) {
		return "undefined"
	}
	if goja.IsNull(v) {
		return "null"
	}
	if _, ok := v.Export().(string); ok {
		return v.String()
	}
	if out, err := json.Marshal(v.Export()); err == nil {
		return string(out)
	}
	return v.String()
}
