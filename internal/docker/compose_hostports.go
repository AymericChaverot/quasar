package docker

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"quasar/internal/db"
)

// HostPort is a port, or a range of them, that a stack publishes straight onto
// the host: the address somebody connects to without going through Traefik.
//
// Only the servers that do not speak HTTP ever have one: a web app is routed by
// Host header, and the adaptation drops the edge bindings. What is left is the
// game servers and the databases — the entries whose address somebody has to
// be given, since there is no link to click.
type HostPort struct {
	Service string
	// IP is the address the port is bound on, "" when it is every address.
	IP     string
	Lo, Hi int
	Proto  string // "tcp" or "udp"
}

// Public is a port bound on every address, and so reachable from outside the
// server — unless a firewall in front of it says otherwise.
func (p HostPort) Public() bool {
	return p.IP == "" || p.IP == "0.0.0.0" || p.IP == "::"
}

// Ports is the port as it is written after a host: "2456" or "2456-2458".
func (p HostPort) Ports() string {
	if p.Hi > p.Lo {
		return fmt.Sprintf("%d-%d", p.Lo, p.Hi)
	}
	return strconv.Itoa(p.Lo)
}

// Address is where to connect: host, or the address the port is bound on when
// it is only one, then the port. A range is written whole for reading, and
// Dial gives the first port of it, which is the one to type.
func (p HostPort) Address(host string) string {
	return p.addr(host) + ":" + p.Ports()
}

// Dial is the address to copy: the first port of a range, since that is the one
// a game client asks for — a server on 2456-2458 is joined at 2456.
func (p HostPort) Dial(host string) string {
	return p.addr(host) + ":" + strconv.Itoa(p.Lo)
}

func (p HostPort) addr(host string) string {
	if p.Public() {
		return host
	}
	if strings.Contains(p.IP, ":") && !strings.HasPrefix(p.IP, "[") {
		return "[" + p.IP + "]"
	}
	return p.IP
}

// Clashes reports two bindings the daemon could not both make: the same
// protocol, overlapping ports, and addresses that overlap — one of them every
// address, or both the same one. 19132/udp and 19132/tcp live side by side, as
// do 127.0.0.1:5432 and 10.0.0.2:5432.
func (p HostPort) Clashes(o HostPort) bool {
	if p.Proto != o.Proto || p.Hi < o.Lo || o.Hi < p.Lo {
		return false
	}
	return p.Public() || o.Public() || p.IP == o.IP
}

// HostPortsIn lists the host ports a compose file binds, with its ${VAR}
// references resolved from the env the app will run with — so a stack that
// parameterises its port is read as the port it will really bind.
//
// This answers a different question from checkComposePorts in compose_ports.go.
// That one asks the daemon, at deploy time, whether a stack collides with
// Traefik's own 80 and 443. This one is a read of the file alone, before
// anything is created, so the form can say that another application already
// holds the port, and the pages can say where the stack is reached. The
// bindings on 80 and 443 are left out: the adaptation drops them, and they are
// never what the stack ends up publishing.
//
// A file that will not parse yields nothing. The deploy reports that far better
// than a port list can.
//
// choice and appPort are the app's routed service and port, which decide what
// the adaptation also takes out: the routed service's own publish of the port
// it is routed on.
func HostPortsIn(compose string, env map[string]string, choice string, appPort int) []HostPort {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(compose), &doc); err != nil {
		return nil
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil
	}
	servicesNode := mapValue(doc.Content[0], "services")
	if servicesNode == nil || servicesNode.Kind != yaml.MappingNode {
		return nil
	}

	services := readServices(servicesNode, env)
	// Decided the way adaptCompose decides it. A file its author wired to
	// Traefik is run as written, so only the edge ports go.
	door, routed := "", 0
	if !anyTraefikLabels(services) {
		if web := pickWebService(services, choice, appPort); web != nil {
			door, routed = proxiedDoor(services, web, web.routedPort(appPort))
		}
	}
	var out []HostPort
	for _, s := range services {
		for _, p := range s.ports {
			if p.hostLo == 0 || dropsPort(s, p, door, routed) {
				continue
			}
			out = append(out, HostPort{Service: s.name, IP: p.hostIP, Lo: p.hostLo, Hi: p.hostHi, Proto: p.proto})
		}
	}
	return out
}

// HostPortsFor is HostPortsIn for an application as it is now: the compose file
// pasted into Quasar, or the one in a git app's checkout. Anything that is not a
// stack publishes nothing.
func (c *Client) HostPortsFor(a *db.App) []HostPort {
	if !c.UsesCompose(a) {
		return nil
	}
	env := envMap(a.EnvContent)
	if a.DeployType == "compose" {
		return HostPortsIn(a.ComposeYAML, env, a.ComposeService, a.Port)
	}
	_, src := c.composeSource(a)
	if src == "" {
		return nil
	}
	raw, err := os.ReadFile(src)
	if err != nil {
		return nil
	}
	return HostPortsIn(string(raw), env, a.ComposeService, a.Port)
}

// anyTraefikLabels reports a file whose author wired it to Traefik already.
func anyTraefikLabels(services []composeService) bool {
	for _, s := range services {
		if s.hasTraefikLabels() {
			return true
		}
	}
	return false
}

// EnvMap parses .env content into what compose interpolates ${VAR} with.
func EnvMap(content string) map[string]string { return envMap(content) }
