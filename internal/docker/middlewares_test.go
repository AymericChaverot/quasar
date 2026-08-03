package docker

import (
	"strings"
	"testing"

	"quasar/internal/db"
)

func labelsFor(a *db.App) map[string]string {
	return (&Client{domain: "example.com"}).traefikLabels(a)
}

func baseApp() *db.App {
	return &db.App{ID: "a1", Subdomain: "app", Port: 8080}
}

// An app with nothing configured must not get a middlewares label at all:
// naming a middleware that does not exist makes Traefik drop the router, taking
// the app offline.
func TestNoMiddlewareChainWhenNothingConfigured(t *testing.T) {
	labels := labelsFor(baseApp())
	if v, ok := labels["traefik.http.routers.qs-a1.middlewares"]; ok {
		t.Errorf("unconfigured app got a middleware chain: %q", v)
	}
}

// Order is the security property: an address that is not allowed through must
// be rejected before it can consume a rate-limit slot or force a bcrypt
// comparison against the basic-auth hash.
func TestMiddlewareChainOrder(t *testing.T) {
	a := baseApp()
	a.IPAllowCIDRs = "203.0.113.0/24"
	a.RateLimit = 10
	a.BasicAuthUser = "ops"
	a.BasicAuthHash = "$2y$05$hash"
	a.SecurityHeaders = true

	got := labelsFor(a)["traefik.http.routers.qs-a1.middlewares"]
	want := "qs-a1-ipallow,qs-a1-ratelimit,qs-a1-auth,qs-a1-headers"
	if got != want {
		t.Errorf("chain = %q, want %q", got, want)
	}
}

func TestEachMiddlewareIsIndependent(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*db.App)
		wantChain string
		wantLabel string
		wantValue string
	}{
		{
			name:      "rate limit only",
			configure: func(a *db.App) { a.RateLimit = 25 },
			wantChain: "qs-a1-ratelimit",
			wantLabel: "traefik.http.middlewares.qs-a1-ratelimit.ratelimit.average",
			wantValue: "25",
		},
		{
			name:      "allowlist only",
			configure: func(a *db.App) { a.IPAllowCIDRs = "10.0.0.0/8, 203.0.113.4/32" },
			wantChain: "qs-a1-ipallow",
			wantLabel: "traefik.http.middlewares.qs-a1-ipallow.ipallowlist.sourcerange",
			wantValue: "10.0.0.0/8,203.0.113.4/32",
		},
		{
			name:      "headers only",
			configure: func(a *db.App) { a.SecurityHeaders = true },
			wantChain: "qs-a1-headers",
			wantLabel: "traefik.http.middlewares.qs-a1-headers.headers.stsSeconds",
			wantValue: "31536000",
		},
		{
			name: "basic auth only",
			configure: func(a *db.App) {
				a.BasicAuthUser, a.BasicAuthHash = "ops", "$2y$05$hash"
			},
			wantChain: "qs-a1-auth",
			wantLabel: "traefik.http.middlewares.qs-a1-auth.basicauth.users",
			wantValue: "ops:$2y$05$hash",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := baseApp()
			tc.configure(a)
			labels := labelsFor(a)
			if got := labels["traefik.http.routers.qs-a1.middlewares"]; got != tc.wantChain {
				t.Errorf("chain = %q, want %q", got, tc.wantChain)
			}
			if got := labels[tc.wantLabel]; got != tc.wantValue {
				t.Errorf("%s = %q, want %q", tc.wantLabel, got, tc.wantValue)
			}
		})
	}
}

// Burst has to exceed the average or a normal page load, which fires several
// requests at once, would rate-limit itself.
func TestRateLimitBurstExceedsAverage(t *testing.T) {
	a := baseApp()
	a.RateLimit = 10
	labels := labelsFor(a)
	average := labels["traefik.http.middlewares.qs-a1-ratelimit.ratelimit.average"]
	burst := labels["traefik.http.middlewares.qs-a1-ratelimit.ratelimit.burst"]
	if average != "10" || burst != "30" {
		t.Errorf("average=%q burst=%q, want 10 and 30", average, burst)
	}
}

// An allowlist of only blank entries must leave the middleware off rather than
// emit an empty source range, which would reject every request.
func TestBlankAllowListIsIgnored(t *testing.T) {
	a := baseApp()
	a.IPAllowCIDRs = " , ,  "
	labels := labelsFor(a)
	if v, ok := labels["traefik.http.routers.qs-a1.middlewares"]; ok {
		t.Errorf("a blank allowlist produced a chain: %q", v)
	}
	for k := range labels {
		if strings.Contains(k, "ipallowlist") {
			t.Errorf("a blank allowlist produced label %q", k)
		}
	}
}

// The panel tells an operator whether the credentials they saved are actually
// in front of the app, and it is this comparison that decides. Getting it wrong
// in the reassuring direction would have the panel call an app protected while
// its container still lets everyone through.
func TestBasicAuthAppliedComparesStoredCredentialsToTheContainer(t *testing.T) {
	protected := baseApp()
	protected.BasicAuthUser, protected.BasicAuthHash = "ops", "$2y$05$hash"

	withAuth := labelsFor(protected)
	noAuth := labelsFor(baseApp())

	rotated := baseApp()
	rotated.BasicAuthUser, rotated.BasicAuthHash = "ops", "$2y$05$other"

	tests := []struct {
		name   string
		app    *db.App
		labels map[string]string
		want   bool
	}{
		{"protected app on a container carrying the same credentials", protected, withAuth, true},
		{"protected app on a container created before it was set", protected, noAuth, false},
		{"password rotated since the container was created", rotated, withAuth, false},
		{"open app on a container that never had a password", baseApp(), noAuth, true},
		{"protection removed but the container still enforces it", baseApp(), withAuth, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := basicAuthApplied(tc.app, tc.labels); got != tc.want {
				t.Errorf("basicAuthApplied = %v, want %v", got, tc.want)
			}
		})
	}
}
