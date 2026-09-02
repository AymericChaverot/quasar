package server

import (
	"net/http"
	"strings"

	"quasar/internal/certs"
	"quasar/internal/db"
	"quasar/internal/docker"
	"quasar/internal/vps"
)

func (s *Server) handleSystemPartial(w http.ResponseWriter, r *http.Request) {
	stats, _ := vps.Collect(s.cfg.HostRootPath)
	s.renderPartial(w, "system_stats", stats)
}

func (s *Server) handleAppsPartial(w http.ResponseWriter, r *http.Request) {
	apps, err := db.ListApps(s.db, s.keyring)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	views := make([]AppView, 0, len(apps))
	for i, a := range apps {
		v := s.appView(r, a)
		v.First = i == 0
		v.Last = i == len(apps)-1
		views = append(views, v)
	}
	s.renderPartial(w, "apps_table", views)
}

// handleSystemContainersPartial lists Quasar's own containers, read-only.
func (s *Server) handleSystemContainersPartial(w http.ResponseWriter, r *http.Request) {
	s.renderPartial(w, "system_containers", s.dock.SystemContainers(r.Context()))
}

// handleDeployFields swaps in the form fields specific to the chosen deploy type.
func (s *Server) handleDeployFields(w http.ResponseWriter, r *http.Request) {
	t := r.URL.Query().Get("type")
	switch t {
	case "image", "git", "compose":
		s.renderPartial(w, "deploy_fields_"+t, map[string]any{})
	default:
		http.Error(w, "unknown type", http.StatusBadRequest)
	}
}

func (s *Server) handleAppStatsPartial(w http.ResponseWriter, r *http.Request) {
	a := s.getApp(w, r)
	if a == nil {
		return
	}
	stats, err := s.dock.Stats(r.Context(), a)
	if err != nil {
		s.renderPartial(w, "container_stats_unavailable", nil)
		return
	}
	s.renderPartial(w, "container_stats", stats)
}

// handleAppContainersPartial lists the containers of a stack, polled so a
// service that dies after the deploy shows up without a reload.
func (s *Server) handleAppContainersPartial(w http.ResponseWriter, r *http.Request) {
	a := s.getApp(w, r)
	if a == nil {
		return
	}
	s.renderPartial(w, "app_containers", map[string]any{
		"AppID":      a.ID,
		"Containers": s.dock.AppContainers(r.Context(), a),
	})
}

// handleAppContainerStatsPartial reports one stack container's resource use,
// as opposed to the whole project's sum shown on the app page.
func (s *Server) handleAppContainerStatsPartial(w http.ResponseWriter, r *http.Request) {
	a := s.getApp(w, r)
	if a == nil {
		return
	}
	ac := s.getAppContainer(w, r, a)
	if ac == nil {
		return
	}
	stats, err := s.dock.StatsByName(r.Context(), ac.Name)
	if err != nil {
		s.renderPartial(w, "container_stats_unavailable", nil)
		return
	}
	s.renderPartial(w, "container_stats", stats)
}

func (s *Server) handleAppStatusPartial(w http.ResponseWriter, r *http.Request) {
	a := s.getApp(w, r)
	if a == nil {
		return
	}
	s.renderPartial(w, "app_status_panel", s.appView(r, a))
}

// handleAppBasicAuthPartial re-reports whether the running container enforces
// the app's password protection. The panel polls it so that a redeploy — which
// is what carries the setting out — is seen to have applied it, instead of
// leaving "redeploy owed" on screen until someone reloads the page.
func (s *Server) handleAppBasicAuthPartial(w http.ResponseWriter, r *http.Request) {
	a := s.getApp(w, r)
	if a == nil {
		return
	}
	s.renderPartial(w, "basic_auth_state", s.appDetailView(r, a))
}

// handleAppLimitsPartial re-reports what the running container allows itself.
// The panel polls it so a redeploy — the only thing that can lift a limit —
// is seen to have done so, rather than leaving "redeploy owed" on screen until
// someone reloads the page.
func (s *Server) handleAppLimitsPartial(w http.ResponseWriter, r *http.Request) {
	a := s.getApp(w, r)
	if a == nil {
		return
	}
	v := s.appView(r, a)
	v.Limits = s.dock.Limits(r.Context(), a)
	s.renderPartial(w, "limits_state", v)
}

// TLSView is the certificate state of every hostname an app answers on, next
// to the route Traefik actually holds for it.
type TLSView struct {
	AppID        string
	Checks       []certs.HostCheck
	Route        docker.RouteInfo
	RouteProblem string
	TraefikNet   string
	Missing      bool // at least one hostname has no certificate
	IsAdmin      bool
}

// handleAppTLSPartial reports why an app is or is not served over HTTPS.
// Loaded lazily, since it makes DNS queries a page render should not block on.
func (s *Server) handleAppTLSPartial(w http.ResponseWriter, r *http.Request) {
	a := s.getApp(w, r)
	if a == nil {
		return
	}
	host := appHost(a, s.cfg.Domain)
	hosts := append([]string{host}, a.CustomDomainList()...)
	route := s.dock.Route(r.Context(), a)
	checks := certs.Diagnose(r.Context(), s.heldCerts(), s.cfg.Domain, hosts)

	missing := false
	for _, c := range checks {
		missing = missing || !c.HasCert
	}
	s.renderPartial(w, "tls_status", TLSView{
		AppID:        a.ID,
		Checks:       checks,
		Route:        route,
		RouteProblem: routeProblem(route, s.dock.UsesCompose(a), host, s.dock.Network()),
		TraefikNet:   s.dock.Network(),
		Missing:      missing,
		IsAdmin:      s.isAdmin(r),
	})
}

// routeProblem explains why Traefik has no working route for the app's
// hostname.
//
// This is the confusing failure: a name Traefik has no router for is still
// answered — with Traefik's own self-signed certificate, the one the browser
// reports as TRAEFIK DEFAULT CERT — and the ACME store stays empty because
// nothing ever asked for a real one. It looks like Let's Encrypt refused when
// it was never called.
func routeProblem(r docker.RouteInfo, usesCompose bool, host, traefikNet string) string {
	switch {
	case !r.HasContainer:
		return "This application has no container, so Traefik has no route for " + host +
			" and answers it with its own default certificate. Deploy the application."
	case !r.Enabled && usesCompose:
		return "No container in this compose project carries traefik.enable=true. Quasar labels the " +
			"service it works out fronts the stack automatically — see Routing above for what it decided, " +
			"or whether the file left it ambiguous. A compose file whose author already wrote its own " +
			"Traefik labels is left exactly as written, so check those carry a Host(`" + host + "`) rule, " +
			"the websecure entrypoint, tls.certresolver=letsencrypt, and membership of the external " +
			traefikNet + " network."
	case !r.Enabled:
		return "The running container carries no Traefik labels, so nothing routes " + host +
			". Redeploy the application."
	case len(r.Rules) == 0:
		return "The container is exposed to Traefik but declares no router rule, so nothing routes " + host + "."
	case !strings.Contains(r.Rule(), "`"+host+"`"):
		return "Traefik routes this container on " + r.Rule() + ", which does not cover " + host +
			". Redeploy the application so its labels are rebuilt."
	case r.CertResolver == "":
		return "The router has no tls.certresolver, so Traefik serves " + host +
			" with its default certificate and never asks Let's Encrypt for a real one."
	case !r.OnTraefikNet:
		return "The container is not attached to the " + traefikNet +
			" network, so Traefik cannot reach it."
	}
	return ""
}

// DeploymentView adds template-friendly context to a deployment row.
type DeploymentView struct {
	*db.Deployment
	CanRollback bool

	// ByCompose is a rollback that goes back to a compose file rather than to
	// an image tag, which is what a stack has to do. The button posts one or
	// the other, and this is which.
	ByCompose bool
}

func (s *Server) handleAppDeploymentsPartial(w http.ResponseWriter, r *http.Request) {
	a := s.getApp(w, r)
	if a == nil {
		return
	}
	deps, err := db.ListDeployments(s.db, a.ID, 10)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// What is running now is not something to go back to. For a single
	// container that is the most recent successful tag; for a stack it is the
	// most recent successful deployment, whatever its file said.
	currentTag, current := "", int64(0)
	for _, d := range deps {
		if d.Status != "success" {
			continue
		}
		if current == 0 {
			current = d.ID
		}
		if currentTag == "" && d.ImageTag != "" {
			currentTag = d.ImageTag
		}
	}
	stack := s.dock.UsesCompose(a)

	views := make([]DeploymentView, 0, len(deps))
	seen := map[string]bool{}
	for _, d := range deps {
		v := DeploymentView{Deployment: d}
		switch {
		// A stack goes back to the file it ran, and only one that kept a copy
		// can be gone back to: deployments from before this was recorded have
		// none, and say so by not offering the button.
		case stack:
			v.CanRollback = d.Status == "success" && d.HasCompose && d.ID != current
			v.ByCompose = v.CanRollback
		case d.Status == "success" && d.ImageTag != "" && d.ImageTag != currentTag && !seen[d.ImageTag]:
			v.CanRollback = true
			seen[d.ImageTag] = true
		}
		views = append(views, v)
	}
	s.renderPartial(w, "deployments", map[string]any{"AppID": a.ID, "Deployments": views, "IsAdmin": s.isAdmin(r)})
}
