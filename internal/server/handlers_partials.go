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

func (s *Server) handleAppStatusPartial(w http.ResponseWriter, r *http.Request) {
	a := s.getApp(w, r)
	if a == nil {
		return
	}
	s.renderPartial(w, "app_status_panel", s.appView(r, a))
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
		return "No container in this compose project carries traefik.enable=true. Quasar does not write " +
			"compose files, so the service that serves HTTP has to carry its own Traefik labels: a " +
			"Host(`" + host + "`) rule, the websecure entrypoint, tls.certresolver=letsencrypt, and " +
			"membership of the external " + traefikNet + " network."
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
	// The most recent successful tag is what's currently running; older
	// successful tags with a distinct image are rollback candidates.
	currentTag := ""
	for _, d := range deps {
		if d.Status == "success" && d.ImageTag != "" {
			currentTag = d.ImageTag
			break
		}
	}
	views := make([]DeploymentView, 0, len(deps))
	seen := map[string]bool{}
	for _, d := range deps {
		v := DeploymentView{Deployment: d}
		if d.Status == "success" && d.ImageTag != "" && d.ImageTag != currentTag && !seen[d.ImageTag] {
			v.CanRollback = !s.dock.UsesCompose(a)
			seen[d.ImageTag] = true
		}
		views = append(views, v)
	}
	s.renderPartial(w, "deployments", map[string]any{"AppID": a.ID, "Deployments": views, "IsAdmin": s.isAdmin(r)})
}
