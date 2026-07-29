package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"quasar/internal/db"
)

// edgePorts are the host ports Traefik binds for the whole platform. A stack
// publishing one of them cannot start — the daemon refuses the second bind —
// and no amount of retrying will change that. Ordered, so a single port range
// swallowing both is always reported the same way.
var edgePorts = []struct {
	Port  int
	Proto string
}{{80, "HTTP"}, {443, "HTTPS"}}

// composeModel is the part of `docker compose config --format json` this needs:
// the resolved project, with variables interpolated and short port syntax
// ("80:80") already expanded into fields.
type composeModel struct {
	Services map[string]struct {
		Ports []struct {
			Published any `json:"published"` // string in recent compose, number in older
		} `json:"ports"`
	} `json:"services"`
}

// portConflict is a service publishing a host port Traefik already binds.
type portConflict struct {
	Service string
	Port    int
	Proto   string
}

// checkComposePorts refuses a stack that would collide with Traefik on the
// host's HTTP ports.
//
// This is the mistake a self-contained repository walks into: its compose file
// puts its own nginx in front of the app and publishes port 80, which is right
// on a laptop and impossible here, where Traefik owns the edge and reaches apps
// over the Docker network instead. Compose starts services in dependency order
// and only fails when it reaches the offending one, so without this the
// operator pays a full build and a half-started stack to find out.
func (c *Client) checkComposePorts(ctx context.Context, a *db.App) error {
	out, err := c.composeOutput(ctx, a, "config", "--format", "json")
	if err != nil {
		// A compose file that cannot even be resolved is not this check's to
		// report: `up` fails on it immediately, and says why in its own words.
		return nil
	}
	var model composeModel
	if err := json.Unmarshal(out, &model); err != nil {
		return nil // an unexpected shape must not block a deploy that would work
	}
	if conflict := findPortConflict(model); conflict.Service != "" {
		return c.portConflictError(conflict, c.ComposeAdaptationFor(a))
	}
	return nil
}

// findPortConflict returns the first service that collides with Traefik, or a
// zero value when the stack is fine. Services are considered in sorted order so
// a stack with two of them names the same one every deploy, rather than
// alternating with Go's map order.
func findPortConflict(model composeModel) portConflict {
	names := make([]string, 0, len(model.Services))
	for name := range model.Services {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		for _, p := range model.Services[name].Ports {
			lo, hi := publishedRange(p.Published)
			for _, edge := range edgePorts {
				if edge.Port >= lo && edge.Port <= hi {
					return portConflict{Service: name, Port: edge.Port, Proto: edge.Proto}
				}
			}
		}
	}
	return portConflict{}
}

// portConflictError explains the collision and what to do about it. "Port is
// already allocated", which is all the daemon says, leaves the operator to work
// out both who holds the port and why publishing it was wrong here.
//
// Reaching this at all means the rewrite in compose_adapt.go left the binding
// in place, and the way out depends on why it did — so the reading it made of
// the file is what the message is built from.
func (c *Client) portConflictError(conflict portConflict, rep ComposeAdaptation) error {
	remedy := fmt.Sprintf("Remove the published port and let Traefik reach the service over the network instead: "+
		"put it on the external %s network and give it the Traefik labels (a Host rule, the websecure "+
		"entrypoint, tls.certresolver=letsencrypt)", c.network)
	switch {
	case rep.Author:
		remedy = fmt.Sprintf("This compose file carries its own Traefik labels, so Quasar runs it exactly as "+
			"written and left the binding alone. Remove it — Traefik reaches %q over the %s network, which "+
			"the file already puts it on", rep.Service, c.network)
	case rep.Ambiguous:
		remedy = "Quasar rewrites a stack's compose file to run behind Traefik, and would have removed this " +
			"binding, but several services could be the one serving the site and nothing singles one out. " +
			"Choose it in the application's Routing panel and redeploy"
	case rep.Gone:
		remedy = fmt.Sprintf("Quasar rewrites a stack's compose file to run behind Traefik, and would have "+
			"removed this binding, but this application is routed to a %q service the file no longer has. "+
			"Choose one it does have in the Routing panel and redeploy", rep.Choice)
	}
	return fmt.Errorf("service %q publishes host port %d, which Traefik already binds to serve %s for every "+
		"application on this server — the Docker daemon refuses the second bind, so the stack cannot start. %s",
		conflict.Service, conflict.Port, conflict.Proto, remedy)
}

// publishedRange reads the host port(s) out of a resolved compose port entry.
// The field is a string in recent compose versions and a number in older ones,
// and may name a range ("8000-8010"). It returns 0, 0 for a service that
// publishes nothing on the host.
func publishedRange(v any) (lo, hi int) {
	if v == nil {
		return 0, 0
	}
	s := strings.TrimSpace(fmt.Sprintf("%v", v))
	if s == "" || s == "0" {
		return 0, 0
	}
	first, last, isRange := strings.Cut(s, "-")
	lo, err := strconv.Atoi(strings.TrimSpace(first))
	if err != nil {
		return 0, 0
	}
	if !isRange {
		return lo, lo
	}
	hi, err = strconv.Atoi(strings.TrimSpace(last))
	if err != nil || hi < lo {
		return lo, lo
	}
	return lo, hi
}
