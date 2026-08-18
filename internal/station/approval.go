package station

// What an operator is asked to accept, and how Quasar remembers that they did.
//
// The rule this file exists for is that a document whose permissions change
// does not take effect until it is re-approved. It is the rule that makes the
// whole model worth anything: a station imported by URL is re-fetched by hand
// later, and if a new revision quietly added net.external, the operator would
// be handing out a capability they never granted. Comparing hashes is how the
// re-fetch knows which of the two it is looking at.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Grant is one permission in the words the install screen uses. The point of
// the plain-words rendering is that consent to something unreadable is not
// consent: "net.external: {allow: [...]}" is a YAML key, and "May reach
// api.modrinth.com over the internet" is a decision somebody can actually make.
type Grant struct {
	// What the station may do.
	Title string

	// Detail is what it may do it to, listed by name — the services, the
	// paths, the hosts. Every permission narrows to something, and this is
	// that something.
	Detail string

	// Strong marks the two an operator should look at twice: running commands
	// in the container is root on it by design, and reaching the internet is
	// an exfiltration channel with their signature on it.
	Strong bool
}

// Granted reports whether the document asks for anything at all.
func (p Permissions) Granted() bool { return len(p.Summary()) > 0 }

// Summary is the permissions in plain words, in the order the install screen
// reads best: what it can run, what it can read and change, where it can
// reach, and what it can do to the application.
func (p Permissions) Summary() []Grant {
	var out []Grant
	add := func(strong bool, title, detail string) {
		out = append(out, Grant{Title: title, Detail: detail, Strong: strong})
	}
	list := func(v []string) string { return strings.Join(v, ", ") }

	if len(p.Exec.Services) > 0 {
		add(true, "Run any command inside the container", list(p.Exec.Services))
	}
	if len(p.Logs.Services) > 0 {
		add(false, "Read the container's logs", list(p.Logs.Services))
	}
	if len(p.Files.Paths) > 0 {
		add(false, "Read and change files under the application's own folder", list(p.Files.Paths))
	}
	if len(p.Env.Read) > 0 {
		add(false, "Read these environment values", list(p.Env.Read))
	}
	if len(p.Env.Write) > 0 {
		add(false, "Change these environment values", list(p.Env.Write))
	}
	if len(p.NetInternal.Services) > 0 {
		ports := make([]string, len(p.NetInternal.Ports))
		for i, n := range p.NetInternal.Ports {
			ports[i] = strconv.Itoa(n)
		}
		add(false, "Talk to its own containers, on this server only",
			fmt.Sprintf("%s on %s", list(p.NetInternal.Services), list(ports)))
	}
	if len(p.NetExternal.Allow) > 0 {
		add(true, "Reach these addresses over the internet, and no others", list(p.NetExternal.Allow))
	}
	if len(p.Lifecycle) > 0 {
		add(false, "Start, stop and redeploy this application", list(p.Lifecycle))
	}
	if p.Notify {
		add(false, "Send messages to your notification webhook", "")
	}
	return out
}

// Hash identifies a set of permissions, so a re-fetched document can be asked
// whether it wants anything the operator has not already accepted.
//
// The lists are sorted first. Writing `services: [b, a]` where the accepted
// revision wrote `[a, b]` grants nothing new, and holding a station back over
// it would train an operator to accept without reading — which is the failure
// this whole mechanism exists to avoid.
func (p Permissions) Hash() string {
	var b strings.Builder
	line := func(key string, values []string) {
		if len(values) == 0 {
			return
		}
		sorted := slices.Clone(values)
		slices.Sort(sorted)
		fmt.Fprintf(&b, "%s=%s\n", key, strings.Join(sorted, ","))
	}

	line("exec", p.Exec.Services)
	line("logs", p.Logs.Services)
	line("files", p.Files.Paths)
	line("env.read", p.Env.Read)
	line("env.write", p.Env.Write)
	line("net.internal.services", p.NetInternal.Services)
	ports := make([]string, len(p.NetInternal.Ports))
	for i, n := range p.NetInternal.Ports {
		ports[i] = strconv.Itoa(n)
	}
	line("net.internal.ports", ports)
	line("net.external", p.NetExternal.Allow)
	line("lifecycle", p.Lifecycle)
	if p.Notify {
		b.WriteString("notify=true\n")
	}

	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}
