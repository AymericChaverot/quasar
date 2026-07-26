package docker

import (
	"context"
	"sort"
	"strings"

	"quasar/internal/db"
)

// RouteInfo is what Traefik was actually told about an app: the labels its
// running container carries and the networks it sits on.
//
// Traefik acts on those and nothing else, so this is the difference between
// "Let's Encrypt refused" and "Traefik never had a route to ask about" — the
// second serves TRAEFIK DEFAULT CERT and leaves no trace in the ACME store.
type RouteInfo struct {
	HasContainer bool
	Enabled      bool     // carries traefik.enable=true
	Rules        []string // the router rules Traefik sees, in label order
	Port         string   // the service's declared internal port
	CertResolver string
	Networks     []string
	OnTraefikNet bool
}

// Rule joins the router rules for display.
func (r RouteInfo) Rule() string { return strings.Join(r.Rules, ", ") }

// Route inspects the container currently serving an app. A compose app is
// inspected through the service its author put the Traefik labels on, falling
// back to any container of the project so "deployed without labels" stays
// distinguishable from "not deployed".
func (c *Client) Route(ctx context.Context, a *db.App) RouteInfo {
	var id string
	if a.DeployType == "compose" {
		if ct, ok := c.composeWebContainer(ctx, a.ID); ok {
			id = ct.ID
		} else if list := c.composeContainers(ctx, a.ID); len(list) > 0 {
			id = list[0].ID
		}
	} else {
		id, _ = c.appContainer(ctx, a.ID)
	}
	if id == "" {
		return RouteInfo{}
	}
	info, err := c.api.ContainerInspect(ctx, id)
	if err != nil {
		return RouteInfo{}
	}

	out := RouteInfo{HasContainer: true, Enabled: info.Config.Labels["traefik.enable"] == "true"}
	for key, value := range info.Config.Labels {
		if !strings.HasPrefix(key, "traefik.http.") {
			continue
		}
		switch {
		case strings.HasPrefix(key, "traefik.http.routers.") && strings.HasSuffix(key, ".rule"):
			out.Rules = append(out.Rules, value)
		case strings.HasSuffix(key, ".loadbalancer.server.port"):
			out.Port = value
		case strings.HasSuffix(key, ".tls.certresolver"):
			out.CertResolver = value
		}
	}
	// Label maps have no order; sorting keeps the panel stable between reloads.
	sort.Strings(out.Rules)

	for name := range info.NetworkSettings.Networks {
		out.Networks = append(out.Networks, name)
		if name == c.network {
			out.OnTraefikNet = true
		}
	}
	sort.Strings(out.Networks)
	return out
}

// Network is the Docker network Traefik watches, named in diagnostics.
func (c *Client) Network() string { return c.network }
