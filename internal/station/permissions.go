package station

// Every privileged thing a station's script can do sits behind a permission
// the document declares here. Nothing is granted by default: a station with no
// permissions block gets a runtime that can compute and return values, and
// nothing else.
//
// Each one is narrowed by name rather than granted wholesale — services for
// exec, globs for files, hosts for the outside world, keys for env, verbs for
// the lifecycle. A permission an operator cannot picture the consequences of
// is not a permission they can meaningfully accept, and "may reach the
// internet" tells them nothing while "may reach api.modrinth.com" turns the
// install screen into real information.
//
// This file is the shape and what it may say. Enforcing it is the parent
// process's job, on the far side of the worker boundary, and lives with the
// capabilities it polices.

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Permissions is the `permissions:` block.
type Permissions struct {
	// Exec is the strongest one: docker exec in the named services, which is
	// root on the container by design.
	Exec Services `yaml:"exec,omitempty"`

	// Logs is separate from Exec because it is far weaker and far more often
	// all a station actually needs.
	Logs Services `yaml:"logs,omitempty"`

	Files Files `yaml:"files,omitempty"`
	Env   Env   `yaml:"env,omitempty"`

	NetInternal NetInternal `yaml:"net.internal,omitempty"`
	NetExternal NetExternal `yaml:"net.external,omitempty"`

	// Lifecycle lists the verbs the script may drive, and only those.
	Lifecycle []string `yaml:"lifecycle,omitempty"`

	// Notify sends to the configured webhook, rate-limited.
	Notify bool `yaml:"notify,omitempty"`
}

// Services names the compose services a permission covers.
type Services struct {
	Services []string `yaml:"services"`
}

// Files is read and write under apps/<id>/, restricted to the declared globs.
type Files struct {
	Paths []string `yaml:"paths"`
}

// Env is per key rather than wholesale, read and write separately: a station
// has no business reading a database password it did not generate.
type Env struct {
	Read  []string `yaml:"read,omitempty"`
	Write []string `yaml:"write,omitempty"`
}

// NetInternal is HTTP to the application's own containers, on named services
// and named ports.
type NetInternal struct {
	Services []string `yaml:"services"`
	Ports    []int    `yaml:"ports"`
}

// NetExternal is HTTPS to the named hosts, exactly. No wildcards, no plain
// HTTP: this is the one permission that is also an exfiltration channel, and
// it is signed by whoever accepted it.
type NetExternal struct {
	Allow []string `yaml:"allow"`
}

// LifecycleVerbs are the things a station may do to its own application.
var LifecycleVerbs = []string{"start", "stop", "restart", "redeploy", "set_image"}

// AllowsExec reports whether the script may run a command in this service.
func (p Permissions) AllowsExec(service string) bool {
	return slices.Contains(p.Exec.Services, service)
}

// AllowsLogs reports whether the script may read this service's logs.
func (p Permissions) AllowsLogs(service string) bool {
	return slices.Contains(p.Logs.Services, service)
}

// AllowsLifecycle reports whether the script may drive this verb.
func (p Permissions) AllowsLifecycle(verb string) bool {
	return slices.Contains(p.Lifecycle, verb)
}

// envKeyRe is what an environment key may be named.
var envKeyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// serviceRe is what a compose service may be named.
var serviceRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// hostRe is a hostname and nothing more: no scheme, no port, no path.
var hostRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

// validate reports what a permissions block cannot say. A declared permission
// that grants nothing is an error rather than a shrug: an `exec: {}` reads on
// the install screen as a station that runs commands, and it is worth knowing
// which of the two the author meant.
func (p Permissions) validate() []error {
	var errs []error
	add := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf("permissions: %s", fmt.Sprintf(format, args...)))
	}

	for _, s := range []struct {
		what string
		list []string
	}{{"exec", p.Exec.Services}, {"logs", p.Logs.Services}} {
		for _, name := range s.list {
			if !serviceRe.MatchString(name) {
				add("%s names a service %q, which is not a service name", s.what, name)
			}
		}
	}

	for _, path := range p.Files.Paths {
		switch {
		case strings.TrimSpace(path) == "":
			add("files carries an empty path")
		case strings.HasPrefix(path, "/"), strings.Contains(path, `\`), volumeRe.MatchString(path):
			add("files path %q leaves the application's own folder; paths are relative to apps/<id>/", path)
		case slices.Contains(strings.Split(path, "/"), ".."):
			add("files path %q climbs out with ..; paths are relative to apps/<id>/", path)
		}
	}

	for _, e := range []struct {
		what string
		keys []string
	}{{"env.read", p.Env.Read}, {"env.write", p.Env.Write}} {
		for _, k := range e.keys {
			if !envKeyRe.MatchString(k) {
				add("%s names %q, which is not an environment key", e.what, k)
			}
		}
	}

	if len(p.NetInternal.Services) > 0 || len(p.NetInternal.Ports) > 0 {
		if len(p.NetInternal.Services) == 0 {
			add("net.internal names ports but no service to reach them on")
		}
		if len(p.NetInternal.Ports) == 0 {
			add("net.internal names services but no port; it reaches nothing as it stands")
		}
		for _, name := range p.NetInternal.Services {
			if !serviceRe.MatchString(name) {
				add("net.internal names a service %q, which is not a service name", name)
			}
		}
		for _, port := range p.NetInternal.Ports {
			if port <= 0 || port > 65535 {
				add("net.internal port %d is out of range", port)
			}
		}
	}

	for _, host := range p.NetExternal.Allow {
		switch {
		case strings.Contains(host, "*"):
			add("net.external allows %q; a wildcard is not a host an operator can weigh, so name each one", host)
		case strings.Contains(host, "://"), strings.Contains(host, "/"):
			add("net.external allows %q; name the host on its own, without a scheme or a path", host)
		case strings.Contains(host, ":"):
			add("net.external allows %q; a host is named without a port", host)
		case !hostRe.MatchString(host):
			add("net.external allows %q, which is not a hostname", host)
		}
	}

	for _, verb := range p.Lifecycle {
		if !slices.Contains(LifecycleVerbs, verb) {
			add("lifecycle asks for %q; the verbs are %s", verb, strings.Join(LifecycleVerbs, ", "))
		}
	}
	return errs
}

// volumeRe catches a Windows drive letter, which is absolute without a leading
// slash and would otherwise pass the check above.
var volumeRe = regexp.MustCompile(`^[A-Za-z]:`)
