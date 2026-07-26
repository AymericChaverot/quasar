package server

import (
	"net/http"
	"strings"
	"testing"
)

// selfServiceRoutes are the mutating routes a viewer is deliberately allowed:
// each one only affects the caller's own login. Anything else that changes
// state must be admin-gated, and the test below fails if a new route slips in
// without that. Adding an entry here should be a conscious decision.
var selfServiceRoutes = map[string]bool{
	"POST /login":                true, // authentication itself
	"POST /logout":               true,
	"POST /2fa":                  true,
	"POST /theme":                true, // a cookie preference
	"POST /hooks/{id}/{secret}":  true, // authenticated by the app's own secret
	"POST /settings/password":    true, // own password
	"POST /settings/2fa/begin":   true, // own second factor
	"POST /settings/2fa/enable":  true,
	"POST /settings/2fa/disable": true,
}

// privilegedReads are GET routes that hand over more than they appear to, and
// so must be admin-gated despite being reads.
var privilegedReads = []string{
	"GET /system/master-key",     // opens every encrypted value on the platform
	"GET /system/backups/{name}", // every app's data and .env
	"GET /apps/{id}/terminal",    // root shell in a container
	"GET /apps/{id}/terminal/ws", //
	"GET /apps/new",              // leads only to a mutation
}

// newTestServer registers the real route table so the guards map reflects
// production, without needing a database or a Docker daemon.
func newTestServer(t *testing.T) *Server {
	t.Helper()
	s := &Server{guards: map[string]string{}, mux: http.NewServeMux()}
	s.routes()
	return s
}

// Every state-changing route must require an admin, or be an explicit
// self-service exception. This is the check that makes the read-only role
// meaningful: without it, a route added later silently becomes writable by
// viewers.
func TestEveryMutatingRouteRequiresAdmin(t *testing.T) {
	s := newTestServer(t)
	if len(s.guards) == 0 {
		t.Fatal("no routes were registered")
	}

	for pattern, level := range s.guards {
		method, _, ok := strings.Cut(pattern, " ")
		if !ok || method == "GET" {
			continue
		}
		if selfServiceRoutes[pattern] {
			continue
		}
		if level != accessAdmin {
			t.Errorf("%s is registered as %q; a mutating route must be admin-gated "+
				"(or added to selfServiceRoutes with a reason)", pattern, level)
		}
	}
}

func TestPrivilegedReadsRequireAdmin(t *testing.T) {
	s := newTestServer(t)
	for _, pattern := range privilegedReads {
		level, registered := s.guards[pattern]
		if !registered {
			t.Errorf("%s is not registered — was it renamed? Update this list", pattern)
			continue
		}
		if level != accessAdmin {
			t.Errorf("%s is registered as %q, want %q", pattern, level, accessAdmin)
		}
	}
}

// The routes that must stay reachable without a session, so an install can be
// logged into at all and webhooks keep working.
func TestPublicRoutes(t *testing.T) {
	s := newTestServer(t)
	for _, pattern := range []string{
		"GET /login", "POST /login", "GET /2fa", "POST /2fa", "POST /logout",
		"POST /hooks/{id}/{secret}",
	} {
		if got := s.guards[pattern]; got != accessPublic {
			t.Errorf("%s is registered as %q, want %q", pattern, got, accessPublic)
		}
	}
}

// A viewer's whole purpose is seeing the platform, so the read surface must not
// drift behind requireAdmin.
func TestViewerCanReachTheReadSurface(t *testing.T) {
	s := newTestServer(t)
	for _, pattern := range []string{
		"GET /{$}",
		"GET /apps/{id}",
		"GET /apps/{id}/logs",
		"GET /logs",
		"GET /audit",
		"GET /system",
		"GET /settings",
		"GET /partials/apps",
		"GET /partials/metrics",
		"GET /partials/apps/{id}/stats",
	} {
		if got := s.guards[pattern]; got != accessSelf {
			t.Errorf("%s is registered as %q, want %q", pattern, got, accessSelf)
		}
	}
}
