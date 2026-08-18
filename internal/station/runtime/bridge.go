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

	// Behind a permission, and therefore behind the parent: the worker holds
	// no disk, no socket and no database, so each of these is a question
	// rather than an action.
	for name, ns := range map[string]*goja.Object{
		"files": b.files(),
		"env":   b.env(),
		"http":  b.http(),
	} {
		if err := q.Set(name, ns); err != nil {
			return err
		}
	}
	if err := q.Set("exec", b.exec); err != nil {
		return err
	}
	if err := q.Set("logs", b.logs); err != nil {
		return err
	}
	if err := q.Set("service", b.service); err != nil {
		return err
	}
	if err := q.Set("notify", b.notify); err != nil {
		return err
	}

	return vm.Set("quasar", q)
}

// exec runs a command in one of the application's services.
//
// argv, not a shell string. A station interpolates constantly — a mod
// filename, a player name, a value read out of a config file — and a shell in
// this path would turn the first one carrying a semicolon into an injection.
// Nothing between here and the daemon parses it.
func (b *bridge) exec(service string, argv []string, opts goja.Value) goja.Value {
	args := map[string]any{"service": service, "argv": argv}
	if o, ok := opts.(*goja.Object); ok && o != nil {
		if v := o.Get("stdin"); v != nil && !goja.IsUndefined(v) {
			args["stdin"] = v.String()
		}
	}
	return b.ask("exec", args)
}

// logs reads a service's recent output.
func (b *bridge) logs(service string, opts goja.Value) goja.Value {
	args := map[string]any{"service": service}
	if o, ok := opts.(*goja.Object); ok && o != nil {
		if v := o.Get("tail"); v != nil && !goja.IsUndefined(v) {
			args["tail"] = v.ToInteger()
		}
		if v := o.Get("since"); v != nil && !goja.IsUndefined(v) {
			args["since"] = v.String()
		}
	}
	return b.ask("logs", args)
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

	// The verbs, over the parent, which is where the list the operator
	// accepted lives. setImage is spelled in JavaScript here and in snake_case
	// in the document, because each reads as itself where it is written.
	for _, verb := range []string{"start", "stop", "restart", "redeploy"} {
		if err := obj.Set(verb, b.verb(verb)); err != nil {
			return nil, err
		}
	}
	if err := obj.Set("setImage", func(ref string) goja.Value {
		return b.ask("lifecycle", map[string]any{"verb": "set_image", "image": ref})
	}); err != nil {
		return nil, err
	}
	return obj, nil
}

// verb is one lifecycle action, asked for by name.
func (b *bridge) verb(name string) func(goja.FunctionCall) goja.Value {
	return func(goja.FunctionCall) goja.Value {
		return b.ask("lifecycle", map[string]any{"verb": name})
	}
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

// files reads and writes under the application's own folder, restricted by the
// parent to the globs the document declared.
func (b *bridge) files() *goja.Object {
	obj := b.vm.NewObject()
	obj.Set("list", func(path string) goja.Value { return b.ask("files.list", pathArg(path)) })
	obj.Set("read", func(path string) goja.Value { return b.ask("files.read", pathArg(path)) })
	obj.Set("readBytes", func(path string) goja.Value { return b.ask("files.readBytes", pathArg(path)) })
	obj.Set("delete", func(path string) goja.Value { return b.ask("files.delete", pathArg(path)) })
	obj.Set("mkdir", func(path string) goja.Value { return b.ask("files.mkdir", pathArg(path)) })
	obj.Set("write", func(path string, content goja.Value) goja.Value {
		return b.ask("files.write", map[string]any{"path": path, "content": text(content)})
	})
	return obj
}

// env reads and writes the named keys of the application's environment, one
// key at a time because that is how the permission is written.
func (b *bridge) env() *goja.Object {
	obj := b.vm.NewObject()
	obj.Set("get", func(key string) goja.Value {
		return b.ask("env.get", map[string]any{"key": key})
	})
	obj.Set("set", func(key string, value goja.Value) goja.Value {
		return b.ask("env.set", map[string]any{"key": key, "value": text(value)})
	})
	return obj
}

func pathArg(path string) map[string]any { return map[string]any{"path": path} }

// text is a value on its way to a file or an environment line. An array of
// bytes — what readBytes hands back, and what an action downloading a mod
// passes straight through — becomes those bytes rather than "1,2,3".
func text(v goja.Value) string {
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return ""
	}
	if numbers, ok := v.Export().([]any); ok {
		buf := make([]byte, 0, len(numbers))
		for _, n := range numbers {
			switch b := n.(type) {
			case int64:
				buf = append(buf, byte(b))
			case float64:
				buf = append(buf, byte(int64(b)))
			default:
				return v.String()
			}
		}
		return string(buf)
	}
	return v.String()
}

// http is the way out, over the parent, which is where the allowlist lives.
func (b *bridge) http() *goja.Object {
	obj := b.vm.NewObject()
	obj.Set("get", func(url string, opts goja.Value) goja.Value { return b.request("http.get", url, opts) })
	obj.Set("post", func(url string, opts goja.Value) goja.Value { return b.request("http.post", url, opts) })
	return obj
}

// request performs one and hands back a response with a json() on it, because
// the first thing every action does with an answer is parse it.
func (b *bridge) request(capability, url string, opts goja.Value) goja.Value {
	args := map[string]any{"url": url}
	if o, ok := opts.(*goja.Object); ok && o != nil {
		if v := o.Get("headers"); v != nil && !goja.IsUndefined(v) {
			args["headers"] = v.Export()
		}
		if v := o.Get("body"); v != nil && !goja.IsUndefined(v) {
			args["body"] = text(v)
		}
		// A json option is the common case written out: an object to send,
		// with the header that says so.
		if v := o.Get("json"); v != nil && !goja.IsUndefined(v) {
			if body, err := json.Marshal(v.Export()); err == nil {
				args["body"] = string(body)
				headers, _ := args["headers"].(map[string]any)
				if headers == nil {
					headers = map[string]any{}
				}
				headers["Content-Type"] = "application/json"
				args["headers"] = headers
			}
		}
	}

	resp := b.ask(capability, args)
	obj, ok := resp.(*goja.Object)
	if !ok {
		return resp
	}
	obj.Set("json", func() goja.Value {
		body := obj.Get("body")
		if body == nil || goja.IsUndefined(body) {
			return goja.Undefined()
		}
		var v any
		if err := json.Unmarshal([]byte(body.String()), &v); err != nil {
			panic(b.vm.NewGoError(fmt.Errorf("the answer is not JSON: %w", err)))
		}
		return b.vm.ToValue(v)
	})
	return obj
}

// service is the address of one of the application's own containers, handed
// out by the parent for a service and a port the document declared.
func (b *bridge) service(name string, port int) goja.Value {
	return b.ask("service", map[string]any{"service": name, "port": port})
}

// notify sends one message to whatever the operator configured — a webhook, a
// push, an email. Rate-limited by the parent, because a station saying the
// same thing in a loop reaches somebody's phone.
func (b *bridge) notify(message string) goja.Value {
	return b.ask("notify", map[string]any{"message": message})
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
