package server

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"strings"

	"quasar/internal/auth"
	"quasar/internal/db"
	"quasar/internal/vps"
)

// The JSON API exists so a pipeline can drive Quasar. Deploy webhooks only ever
// covered "redeploy this one app"; this covers listing, inspecting and acting.
//
// Nothing here returns an app's environment variables or compose file. A token
// lives in CI configuration, which is a weaker place than the encrypted column
// those values sit in, so the API deliberately cannot read them out.

// apiTokenKey is the context key carrying the authenticated token's name, for
// the audit trail.
type apiTokenKey struct{}

func withAPIToken(r *http.Request, name string) context.Context {
	return context.WithValue(r.Context(), apiTokenKey{}, name)
}

// auditAPI records an action against the token that performed it, so an API
// deploy is attributable to a specific credential rather than to "webhook" or
// nothing at all.
func (s *Server) auditAPI(r *http.Request, action, target, detail string) {
	name, _ := r.Context().Value(apiTokenKey{}).(string)
	if name == "" {
		name = "token"
	}
	s.auditAs(r, "token:"+name, action, target, detail)
}

// round1 keeps API numbers readable instead of shipping float noise.
func round1(v float64) float64 { return math.Round(v*10) / 10 }

// vpsCollect is a variable so tests can stand in for host metrics.
var vpsCollect = vps.Collect

// requireToken authenticates a Bearer token and, when adminOnly, requires that
// the token was issued with the admin role.
func (s *Server) requireToken(adminOnly bool, next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secret := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if secret == "" {
			writeAPIError(w, http.StatusUnauthorized, "missing Authorization: Bearer <token> header")
			return
		}
		name, role, err := auth.AuthenticateToken(s.db, secret)
		if err != nil {
			writeAPIError(w, http.StatusUnauthorized, "invalid token")
			return
		}
		if adminOnly && role != auth.RoleAdmin {
			writeAPIError(w, http.StatusForbidden, "this token is read-only")
			return
		}
		next(w, r.WithContext(withAPIToken(r, name)))
	})
}

func writeAPIError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// apiApp is the shape an app takes over the API: identity, configuration that
// is not secret, and live state.
type apiApp struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Subdomain  string `json:"subdomain"`
	Host       string `json:"host"`
	DeployType string `json:"deploy_type"`
	ImageRef   string `json:"image_ref,omitempty"`
	GitURL     string `json:"git_url,omitempty"`
	GitBranch  string `json:"git_branch,omitempty"`
	Port       int    `json:"port"`
	State      string `json:"state"`
	Uptime     string `json:"uptime,omitempty"`
}

func (s *Server) apiAppFrom(r *http.Request, a *db.App) apiApp {
	v := s.appView(r, a)
	return apiApp{
		ID:         a.ID,
		Name:       a.Name,
		Subdomain:  a.Subdomain,
		Host:       v.Host(),
		DeployType: a.DeployType,
		ImageRef:   a.ImageRef,
		GitURL:     a.GitURL,
		GitBranch:  a.GitBranch,
		Port:       a.Port,
		State:      v.Status.State,
		Uptime:     v.Status.Uptime,
	}
}

func (s *Server) handleAPIApps(w http.ResponseWriter, r *http.Request) {
	apps, err := db.ListApps(s.db, s.keyring)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]apiApp, 0, len(apps))
	for _, a := range apps {
		out = append(out, s.apiAppFrom(r, a))
	}
	writeJSON(w, http.StatusOK, map[string]any{"apps": out})
}

func (s *Server) handleAPIApp(w http.ResponseWriter, r *http.Request) {
	a, err := db.GetApp(s.db, s.keyring, r.PathValue("id"))
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "application not found")
		return
	}
	writeJSON(w, http.StatusOK, s.apiAppFrom(r, a))
}

// handleAPIDeploy triggers a deploy and returns immediately: a deploy outlives
// any sensible request timeout, so the caller polls the app for its state.
//
// It fetches first, like the webhook: what calls this is a pipeline that has
// just pushed something and expects to see it live.
func (s *Server) handleAPIDeploy(w http.ResponseWriter, r *http.Request) {
	a, err := db.GetApp(s.db, s.keyring, r.PathValue("id"))
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "application not found")
		return
	}
	if d := s.dock.Deploying(a.ID); d != nil && d.Running {
		writeAPIError(w, http.StatusConflict, "a deploy is already in progress")
		return
	}
	s.dock.UpdateAsync(a, "api")
	s.auditAPI(r, "app.deploy", a.Name, "triggered by API token")
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "deploying", "app": a.ID})
}

func (s *Server) handleAPIRestart(w http.ResponseWriter, r *http.Request) {
	a, err := db.GetApp(s.db, s.keyring, r.PathValue("id"))
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "application not found")
		return
	}
	if err := s.dock.Restart(r.Context(), a); err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.auditAPI(r, "app.restart", a.Name, "triggered by API token")
	writeJSON(w, http.StatusOK, map[string]string{"status": "restarted", "app": a.ID})
}

// handleAPISystem reports host usage, for an external monitor that would
// otherwise have to scrape the dashboard's HTML.
func (s *Server) handleAPISystem(w http.ResponseWriter, r *http.Request) {
	stats, err := vpsCollect(s.cfg.HostRootPath)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"cpu_percent":   round1(stats.CPUPercent),
		"mem_percent":   round1(stats.MemPercent),
		"mem_used_gb":   round1(stats.MemUsedGB),
		"mem_total_gb":  round1(stats.MemTotalGB),
		"disk_percent":  round1(stats.DiskPercent),
		"disk_used_gb":  round1(stats.DiskUsedGB),
		"disk_total_gb": round1(stats.DiskTotalGB),
	})
}
