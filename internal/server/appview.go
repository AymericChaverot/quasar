package server

import (
	"net/http"

	"quasar/internal/auth"
	"quasar/internal/db"
	"quasar/internal/docker"
)

// An application as the templates see it: the row, plus everything about it
// that is only true right now — whether it is running, what the edge is doing
// in front of it, where it sits in the dashboard's order.
//
// The handlers build one of these and hand it over; nothing in a template
// reaches past it. The handlers themselves are in handlers_apps.go, and the
// per-application settings forms in handlers_app_settings.go.

// AppView bundles an app with its live status for templates. First/Last are
// only set when rendering the ordered dashboard list (they gate the
// move-up/move-down buttons).
type AppView struct {
	*db.App
	Status docker.AppStatus
	Domain string
	Deploy *docker.DeployState
	// Build is how a git app's checkout is deployed, and what the checkout
	// offers. Zero for the other deploy types, which have nothing to choose.
	Build docker.GitBuild
	// Stack is true when the app runs as a compose project, whether its
	// compose file was pasted into Quasar or found in its repository. The
	// page shows a stack's containers individually.
	Stack bool
	// Compose is what Quasar made of a stack's compose file to run it behind
	// Traefik. It reads the file, so it is only filled in for the pages that
	// show it — appDetailView — and left zero for the dashboard list.
	Compose docker.ComposeAdaptation
	// Network is the Docker network Traefik watches, named wherever the page
	// explains how an app is reached.
	Network string
	// AuthPending is true when the app's password protection has been changed
	// since the container serving it was created, so visitors still meet the
	// previous setting. Only filled in for the detail pages, which is where
	// the setting is changed.
	AuthPending bool
	// Limits is what the running container really allows itself, which the
	// panel that sets them reports next to what is stored. Only filled in for
	// the detail pages, for the same reason AuthPending is.
	Limits docker.LimitsState
	// LimitsError is the Engine refusing a live change to those limits. The
	// save itself still landed, so this is shown inside the panel rather than
	// returned as an error.
	LimitsError string
	First       bool
	Last        bool
	// IsAdmin gates the controls inside partials, which are rendered without
	// the page data map that carries it everywhere else.
	IsAdmin bool
}

// Host is the public hostname of the app: "sub.domain", or the bare root
// domain when the app claims the apex via the "@" subdomain.
func (v AppView) Host() string { return appHost(v.App, v.Domain) }

// basicAuthMinLength is the shortest password accepted for edge protection.
const basicAuthMinLength = 4

// BasicAuthMinLength lets the form refuse a too-short password itself. A form
// posted anyway is still rejected by the handler; what this buys is the
// refusal being visible — htmx swaps nothing on a 4xx, so a rejected save
// otherwise looks exactly like a page that ignored the click.
func (v AppView) BasicAuthMinLength() int { return basicAuthMinLength }

// appHost is Host for callers that have an app and the root domain but no view
// to wrap them in.
func appHost(a *db.App, domain string) string {
	if a.Subdomain == "@" {
		return domain
	}
	return a.Subdomain + "." + domain
}

func (s *Server) appView(r *http.Request, a *db.App) AppView {
	_, _, role, _ := s.currentUser(r)
	return AppView{
		App:     a,
		Status:  s.dock.Status(r.Context(), a),
		Domain:  s.cfg.Domain,
		Deploy:  s.dock.Deploying(a.ID),
		Build:   s.dock.GitBuildFor(a),
		Stack:   s.dock.UsesCompose(a),
		Network: s.dock.Network(),
		IsAdmin: role == auth.RoleAdmin,
	}
}

// appDetailView is appView for the pages about one application, which have
// room to say how a stack's compose file is being run. The list does not, and
// reading every app's compose file to render a table nobody asked that of would
// be work for nothing.
func (s *Server) appDetailView(r *http.Request, a *db.App) AppView {
	v := s.appView(r, a)
	v.Compose = s.dock.ComposeAdaptationFor(a)
	v.AuthPending = s.dock.ProtectionPending(r.Context(), a)
	v.Limits = s.dock.Limits(r.Context(), a)
	return v
}

// LimitsText names the limits stored for the app, "unlimited" for none.
func (v AppView) LimitsText() string { return docker.LimitsText(v.CPULimit, v.MemLimitMB) }

// MinMemLimitMB and MinCPULimit let the form state the floors it enforces.
func (v AppView) MinMemLimitMB() int64 { return minMemLimitMB }
func (v AppView) MinCPULimit() float64 { return minCPULimit }
