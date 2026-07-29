package server

import (
	"strings"
	"testing"

	"quasar/internal/docker"
)

// A healthy route: the panel must stay quiet, or every app grows a warning.
func TestRouteProblemSilentWhenRouted(t *testing.T) {
	r := docker.RouteInfo{
		HasContainer: true, Enabled: true,
		Rules:        []string{"Host(`example.com`)"},
		Port:         "80",
		CertResolver: "letsencrypt",
		Networks:     []string{"traefik-net"},
		OnTraefikNet: true,
	}
	if msg := routeProblem(r, false, "example.com", "traefik-net"); msg != "" {
		t.Errorf("routeProblem() = %q, want no complaint", msg)
	}
}

func TestRouteProblem(t *testing.T) {
	routed := func(rule string) docker.RouteInfo {
		return docker.RouteInfo{
			HasContainer: true, Enabled: true, Rules: []string{rule},
			Port: "80", CertResolver: "letsencrypt",
			Networks: []string{"traefik-net"}, OnTraefikNet: true,
		}
	}

	tests := []struct {
		name        string
		route       docker.RouteInfo
		usesCompose bool
		want        string // substring the explanation must carry
	}{
		{
			name:  "never deployed",
			route: docker.RouteInfo{},
			want:  "no container",
		},
		{
			// Quasar labels the service it auto-detects, but a container can still
			// come up unlabelled: the file was ambiguous, its chosen service is
			// gone, or its author wrote their own labels that don't enable routing.
			name:        "compose app without traefik labels",
			route:       docker.RouteInfo{HasContainer: true},
			usesCompose: true,
			want:        "traefik.enable=true",
		},
		{
			name:  "managed container missing its labels",
			route: docker.RouteInfo{HasContainer: true},
			want:  "Redeploy",
		},
		{
			// The apex case: labels built before the app claimed "@" still name
			// the old host, so Traefik answers example.com with its default cert.
			name:  "rule does not cover the host",
			route: routed("Host(`app.example.com`)"),
			want:  "does not cover example.com",
		},
		{
			name: "no certificate resolver",
			route: func() docker.RouteInfo {
				r := routed("Host(`example.com`)")
				r.CertResolver = ""
				return r
			}(),
			want: "never asks Let's Encrypt",
		},
		{
			name: "off the traefik network",
			route: func() docker.RouteInfo {
				r := routed("Host(`example.com`)")
				r.Networks, r.OnTraefikNet = []string{"bridge"}, false
				return r
			}(),
			want: "not attached to the traefik-net network",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := routeProblem(tc.route, tc.usesCompose, "example.com", "traefik-net")
			if !strings.Contains(got, tc.want) {
				t.Errorf("routeProblem() = %q, want it to mention %q", got, tc.want)
			}
		})
	}
}

// A rule naming the apex must not be read as covering a subdomain of it, or the
// exact confusion this panel exists to clear up would go unreported.
func TestRouteProblemMatchesTheWholeHostname(t *testing.T) {
	r := docker.RouteInfo{
		HasContainer: true, Enabled: true,
		Rules:        []string{"Host(`example.com`)"},
		CertResolver: "letsencrypt", OnTraefikNet: true,
	}
	if msg := routeProblem(r, false, "app.example.com", "traefik-net"); msg == "" {
		t.Error("Host(`example.com`) must not count as covering app.example.com")
	}
}
