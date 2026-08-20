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
	"GET /apps/{id}/deploy-log",  // build output, which echoes build args and env

	// A file out of an application's own folder, which is the same material
	// the storage explorer hands over and is gated the same way.
	"GET /apps/{id}/station/download",
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
		// accessTokenAdmin is the API's equivalent: a different credential, the
		// same admin requirement.
		if level != accessAdmin && level != accessTokenAdmin {
			t.Errorf("%s is registered as %q; a mutating route must require admin "+
				"(or be added to selfServiceRoutes with a reason)", pattern, level)
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

// The API must never be reachable with a session cookie alone, and never
// without a token: a browser that is merely logged in is not an API client, and
// a CSRF-able JSON endpoint on the same origin as the dashboard would be.
func TestAPIRoutesRequireAToken(t *testing.T) {
	s := newTestServer(t)
	found := 0
	for pattern, level := range s.guards {
		if !strings.Contains(pattern, "/api/") {
			continue
		}
		found++
		if level != accessTokenRead && level != accessTokenAdmin {
			t.Errorf("%s is registered as %q, want a token guard", pattern, level)
		}
	}
	if found == 0 {
		t.Fatal("no API routes were registered")
	}
}

// Reads are fine for a viewer token; anything that acts on an app needs admin.
func TestAPIWriteRoutesNeedAnAdminToken(t *testing.T) {
	s := newTestServer(t)
	for _, pattern := range []string{
		"POST /api/v1/apps/{id}/deploy",
		"POST /api/v1/apps/{id}/restart",
	} {
		if got := s.guards[pattern]; got != accessTokenAdmin {
			t.Errorf("%s is registered as %q, want %q", pattern, got, accessTokenAdmin)
		}
	}
	for _, pattern := range []string{
		"GET /api/v1/apps",
		"GET /api/v1/apps/{id}",
		"GET /api/v1/system",
	} {
		if got := s.guards[pattern]; got != accessTokenRead {
			t.Errorf("%s is registered as %q, want %q", pattern, got, accessTokenRead)
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

// A page nested under a section — an application, its containers, its
// terminal — has to keep that section lit in the header. Getting this wrong
// leaves no entry marked, which reads as having navigated out of the section.
func TestNavSection(t *testing.T) {
	cases := map[string]string{
		"/":                            "apps",
		"/apps/abcd1234":               "apps",
		"/apps/abcd1234/containers/db": "apps",
		"/apps/abcd1234/terminal":      "apps",
		"/apps/new":                    "new",
		"/logs":                        "logs",
		"/audit":                       "audit",
		"/system":                      "system",
		"/system/containers/traefik":   "system",
		"/settings":                    "settings",
		// Signed out, or a route with no place in the header at all.
		"/login": "",
	}
	for path, want := range cases {
		if got := navSection(path); got != want {
			t.Errorf("navSection(%q) = %q, want %q", path, got, want)
		}
	}
}
