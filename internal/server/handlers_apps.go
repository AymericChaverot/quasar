package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"quasar/internal/catalog"
	"quasar/internal/db"
	"quasar/internal/docker"
)

var (
	subdomainRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	domainRe    = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,}$`)
)

func randomHex(n int) string {
	buf := make([]byte, n)
	rand.Read(buf)
	return hex.EncodeToString(buf)
}

// AppView bundles an app with its live status for templates. First/Last are
// only set when rendering the ordered dashboard list (they gate the
// move-up/move-down buttons).
type AppView struct {
	*db.App
	Status docker.AppStatus
	Domain string
	Deploy *docker.DeployState
	First  bool
	Last   bool
}

// Host is the public hostname of the app: "sub.domain", or the bare root
// domain when the app claims the apex via the "@" subdomain.
func (v AppView) Host() string {
	if v.App.Subdomain == "@" {
		return v.Domain
	}
	return v.App.Subdomain + "." + v.Domain
}

func (s *Server) appView(ctx context.Context, a *db.App) AppView {
	return AppView{
		App:    a,
		Status: s.dock.Status(ctx, a),
		Domain: s.cfg.Domain,
		Deploy: s.dock.Deploying(a.ID),
	}
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "dashboard", map[string]any{"Title": "Dashboard", "Domain": s.cfg.Domain})
}

func (s *Server) handleAppNew(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{
		"Title":   "New application",
		"Domain":  s.cfg.Domain,
		"Catalog": catalog.Templates,
	}
	// ?template=<id> prefills the form from a one-click catalog entry.
	if t := catalog.Get(r.URL.Query().Get("template")); t != nil {
		data["Form"] = &db.App{
			Name:       t.Name,
			Subdomain:  t.ID,
			DeployType: "image",
			ImageRef:   t.ImageRef,
			Port:       t.Port,
			DataMount:  t.DataMount,
			EnvContent: t.RenderEnv(),
		}
	}
	s.render(w, r, "app_new", data)
}

func (s *Server) handleAppCreate(w http.ResponseWriter, r *http.Request) {
	form := func(k string) string { return strings.TrimSpace(r.FormValue(k)) }

	a := &db.App{
		Name:          form("name"),
		Subdomain:     strings.ToLower(form("subdomain")),
		DeployType:    form("deploy_type"),
		ImageRef:      form("image_ref"),
		GitURL:        form("git_url"),
		GitBranch:     form("git_branch"),
		ComposeYAML:   r.FormValue("compose_yaml"),
		EnvContent:    strings.ReplaceAll(r.FormValue("env_content"), "\r\n", "\n"),
		DataMount:     form("data_mount"),
		CustomDomains: normalizeDomains(form("custom_domains")),
		Port:          80,
	}
	if p := form("port"); p != "" {
		if n, err := strconv.Atoi(p); err == nil && n > 0 && n < 65536 {
			a.Port = n
		}
	}
	if v := form("cpu_limit"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 {
			a.CPULimit = f
		}
	}
	if v := form("mem_limit_mb"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
			a.MemLimitMB = n
		}
	}
	if a.GitBranch == "" {
		a.GitBranch = "main"
	}

	if errMsg := s.validateNewApp(a); errMsg != "" {
		s.render(w, r, "app_new", map[string]any{
			"Title": "New application", "Domain": s.cfg.Domain,
			"Error": errMsg, "Form": a,
		})
		return
	}

	a.ID = randomHex(4)
	a.WebhookSecret = randomHex(16)

	if err := db.InsertApp(s.db, s.keyring, a); err != nil {
		http.Error(w, "database error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.dock.DeployAsync(a, "manual")
	s.audit(r, "app.create", a.Name, a.Subdomain+" ("+a.DeployType+")")
	http.Redirect(w, r, "/apps/"+a.ID, http.StatusSeeOther)
}

// normalizeDomains lowercases and deduplicates a comma-separated domain list.
func normalizeDomains(raw string) string {
	var out []string
	seen := map[string]bool{}
	for _, d := range strings.Split(strings.ToLower(raw), ",") {
		if d = strings.TrimSpace(d); d != "" && !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	return strings.Join(out, ",")
}

func (s *Server) validateNewApp(a *db.App) string {
	if a.Name == "" {
		return "Application name is required."
	}
	// "@" claims the root domain itself (DNS apex convention).
	if a.Subdomain != "@" && !subdomainRe.MatchString(a.Subdomain) {
		return "Subdomain must contain only lowercase letters, digits and hyphens (or @ for the root domain)."
	}
	if a.Subdomain == "admin" {
		return "The subdomain \"admin\" is reserved for this dashboard."
	}
	if taken, _ := db.SubdomainTaken(s.db, a.Subdomain); taken {
		return "This subdomain is already used by another application."
	}
	switch a.DeployType {
	case "image":
		if a.ImageRef == "" {
			return "A Docker image reference is required (e.g. nginx:latest)."
		}
	case "git":
		if a.GitURL == "" {
			return "A Git repository URL is required."
		}
	case "compose":
		if strings.TrimSpace(a.ComposeYAML) == "" {
			return "A docker-compose.yml content is required."
		}
	default:
		return "Invalid deployment type."
	}
	if a.DataMount != "" && !strings.HasPrefix(a.DataMount, "/") {
		return "The persistent data mount must be an absolute container path (e.g. /data)."
	}
	for _, d := range a.CustomDomainList() {
		if !domainRe.MatchString(d) {
			return "Invalid custom domain: " + d
		}
	}
	return ""
}

func (s *Server) getApp(w http.ResponseWriter, r *http.Request) *db.App {
	a, err := db.GetApp(s.db, s.keyring, r.PathValue("id"))
	if err != nil {
		http.Error(w, "application not found", http.StatusNotFound)
		return nil
	}
	return a
}

func (s *Server) handleAppDetail(w http.ResponseWriter, r *http.Request) {
	a := s.getApp(w, r)
	if a == nil {
		return
	}
	s.render(w, r, "app_detail", map[string]any{
		"Title": a.Name,
		"App":   s.appView(r.Context(), a),
	})
}

// appAction returns a handler for start/stop/restart, re-rendering the status badge.
func (s *Server) appAction(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a := s.getApp(w, r)
		if a == nil {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()
		var err error
		switch action {
		case "start":
			err = s.dock.Start(ctx, a)
		case "stop":
			err = s.dock.Stop(ctx, a)
		case "restart":
			err = s.dock.Restart(ctx, a)
		}
		if err != nil {
			http.Error(w, fmt.Sprintf("%s failed: %v", action, err), http.StatusInternalServerError)
			return
		}
		s.audit(r, "app."+action, a.Name, "")
		s.renderPartial(w, "app_status_panel", s.appView(r.Context(), a))
	}
}

func (s *Server) handleAppRedeploy(w http.ResponseWriter, r *http.Request) {
	a := s.getApp(w, r)
	if a == nil {
		return
	}
	s.dock.DeployAsync(a, "manual")
	s.audit(r, "app.deploy", a.Name, "")
	s.renderPartial(w, "app_status_panel", s.appView(r.Context(), a))
}

// handleAppRollback redeploys an image tag from the app's deploy history.
func (s *Server) handleAppRollback(w http.ResponseWriter, r *http.Request) {
	a := s.getApp(w, r)
	if a == nil {
		return
	}
	tag := r.FormValue("tag")
	// Only tags recorded in this app's history are accepted.
	deps, _ := db.ListDeployments(s.db, a.ID, 50)
	valid := false
	for _, d := range deps {
		if d.ImageTag != "" && d.ImageTag == tag && d.Status == "success" {
			valid = true
			break
		}
	}
	if !valid {
		http.Error(w, "unknown image tag for this application", http.StatusBadRequest)
		return
	}
	s.dock.RollbackAsync(a, tag)
	s.audit(r, "app.rollback", a.Name, "to "+tag)
	s.renderPartial(w, "app_status_panel", s.appView(r.Context(), a))
}

// handleAppMove shifts an app up or down in the manual order and re-renders
// the list. It rewrites every position, which also heals legacy rows that
// still share the default sort_order of 0.
func (s *Server) handleAppMove(w http.ResponseWriter, r *http.Request) {
	a := s.getApp(w, r)
	if a == nil {
		return
	}
	apps, err := db.ListApps(s.db, s.keyring)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	idx := -1
	for i, other := range apps {
		if other.ID == a.ID {
			idx = i
			break
		}
	}
	switch r.FormValue("dir") {
	case "up":
		if idx > 0 {
			apps[idx-1], apps[idx] = apps[idx], apps[idx-1]
		}
	case "down":
		if idx >= 0 && idx < len(apps)-1 {
			apps[idx+1], apps[idx] = apps[idx], apps[idx+1]
		}
	default:
		http.Error(w, "dir must be up or down", http.StatusBadRequest)
		return
	}
	for i, other := range apps {
		db.SetAppOrder(s.db, other.ID, i+1)
	}
	s.handleAppsPartial(w, r)
}

// handleAppPreBackup saves the command run in the container before a backup,
// whose stdout is archived as the app's dump.
func (s *Server) handleAppPreBackup(w http.ResponseWriter, r *http.Request) {
	a := s.getApp(w, r)
	if a == nil {
		return
	}
	if err := db.UpdateAppPreBackup(s.db, s.keyring, a.ID, strings.TrimSpace(r.FormValue("pre_backup_cmd"))); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.renderPartial(w, "env_saved", nil)
}

// handleAppHealth saves the HTTP health check path (empty disables checks).
func (s *Server) handleAppHealth(w http.ResponseWriter, r *http.Request) {
	a := s.getApp(w, r)
	if a == nil {
		return
	}
	path := strings.TrimSpace(r.FormValue("health_path"))
	if path != "" && !strings.HasPrefix(path, "/") {
		http.Error(w, "health path must start with /", http.StatusBadRequest)
		return
	}
	if err := db.UpdateAppHealth(s.db, a.ID, path); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.renderPartial(w, "env_saved", nil)
}

// handleAppBasicAuth enables/disables Traefik basic auth (applied at redeploy).
func (s *Server) handleAppBasicAuth(w http.ResponseWriter, r *http.Request) {
	a := s.getApp(w, r)
	if a == nil {
		return
	}
	if r.FormValue("disable") == "1" {
		if err := db.UpdateAppBasicAuth(s.db, a.ID, "", ""); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.renderPartial(w, "env_saved", nil)
		return
	}
	user := strings.TrimSpace(r.FormValue("ba_user"))
	pass := r.FormValue("ba_password")
	if user == "" || len(pass) < 4 {
		http.Error(w, "username and a password of at least 4 characters are required", http.StatusBadRequest)
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(pass), bcrypt.DefaultCost)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := db.UpdateAppBasicAuth(s.db, a.ID, user, string(hash)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.renderPartial(w, "env_saved", nil)
}

// handleAppDomains updates the app's custom domain list (redeploy applies it).
func (s *Server) handleAppDomains(w http.ResponseWriter, r *http.Request) {
	a := s.getApp(w, r)
	if a == nil {
		return
	}
	domains := normalizeDomains(r.FormValue("custom_domains"))
	tmp := db.App{CustomDomains: domains}
	for _, d := range tmp.CustomDomainList() {
		if !domainRe.MatchString(d) {
			http.Error(w, "invalid domain: "+d, http.StatusBadRequest)
			return
		}
	}
	if err := db.UpdateAppDomains(s.db, a.ID, domains); err != nil {
		http.Error(w, "database error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.renderPartial(w, "env_saved", nil)
}

func (s *Server) handleAppDelete(w http.ResponseWriter, r *http.Request) {
	a := s.getApp(w, r)
	if a == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := s.dock.Remove(ctx, a); err != nil {
		http.Error(w, "delete failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := db.DeleteApp(s.db, a.ID); err != nil {
		http.Error(w, "database error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	db.DeleteDeployments(s.db, a.ID)
	db.DeleteTasksForApp(s.db, a.ID)
	db.DeleteAppTimeSeries(s.db, a.ID)
	db.DeleteAppLogs(s.db, a.ID)
	// Recorded after the fact and deliberately outside DeleteAppLogs' reach:
	// deleting an app must not also erase the record of who deleted it.
	s.audit(r, "app.delete", a.Name, a.Subdomain+" ("+a.DeployType+")")
	w.Header().Set("HX-Redirect", "/")
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleAppEnvSave(w http.ResponseWriter, r *http.Request) {
	a := s.getApp(w, r)
	if a == nil {
		return
	}
	a.EnvContent = strings.ReplaceAll(r.FormValue("env_content"), "\r\n", "\n")
	if err := db.UpdateAppEnv(s.db, s.keyring, a.ID, a.EnvContent); err != nil {
		http.Error(w, "database error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if a.DeployType == "compose" {
		if yaml := r.FormValue("compose_yaml"); yaml != "" {
			a.ComposeYAML = yaml
			db.UpdateAppCompose(s.db, s.keyring, a.ID, yaml)
		}
	}
	if err := s.dock.WriteEnvFile(a); err != nil {
		http.Error(w, "write .env failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// The values themselves are never recorded — that would put every secret
	// in plaintext in a table the encryption exists to keep them out of.
	s.audit(r, "app.env-change", a.Name, "")
	s.renderPartial(w, "env_saved", nil)
}
