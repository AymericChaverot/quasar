package docker

import (
	"gopkg.in/yaml.v3"
)

// PublishedPorts lists the host ports a compose file binds, with its ${VAR}
// references resolved from the env the app will run with — so a stack that
// parameterises its port is read as the port it will really bind.
//
// This answers a different question from checkComposePorts in compose_ports.go.
// That one asks the daemon, at deploy time, whether a stack collides with
// Traefik's own 80 and 443. This one is a read of the file alone, before
// anything is created, so the form can say that another application already
// holds the port — and it is a read of the file because there is nothing to ask
// the daemon about yet.
//
// Only the servers that do not speak HTTP ever bind one: a web app is routed by
// Host header, and the adaptation drops the edge bindings. What is left is the
// game servers and the databases, which are exactly the entries an operator
// deploys several of.
//
// A file that will not parse yields nothing. The deploy reports that far better
// than a port check can.
func PublishedPorts(compose string, env map[string]string) []int {
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

	var out []int
	for _, s := range readServices(servicesNode, env) {
		for _, p := range s.ports {
			// A range binds every port in it; a single binding has lo == hi.
			for n := p.hostLo; n > 0 && n <= p.hostHi; n++ {
				out = append(out, n)
			}
		}
	}
	return out
}

// EnvMap parses .env content into what compose interpolates ${VAR} with.
func EnvMap(content string) map[string]string { return envMap(content) }
