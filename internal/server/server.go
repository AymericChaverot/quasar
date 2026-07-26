package server

import (
	"database/sql"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"

	"quasar/internal/auth"
	"quasar/internal/config"
	"quasar/internal/docker"
	"quasar/internal/secrets"
	"quasar/internal/version"
	"quasar/web"
)

type Server struct {
	cfg     config.Config
	db      *sql.DB
	dock    *docker.Client
	keyring *secrets.Keyring
	pages   map[string]*template.Template
	mux     *http.ServeMux
}

func New(cfg config.Config, database *sql.DB, dock *docker.Client, keyring *secrets.Keyring) (*Server, error) {
	s := &Server{
		cfg:     cfg,
		db:      database,
		dock:    dock,
		keyring: keyring,
		pages:   map[string]*template.Template{},
		mux:     http.NewServeMux(),
	}
	if err := s.parseTemplates(); err != nil {
		return nil, err
	}
	s.routes()
	return s, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

// parseTemplates builds one template set per page: layout + partials + page.
func (s *Server) parseTemplates() error {
	pageFiles, err := fs.Glob(web.Files, "templates/pages/*.html")
	if err != nil {
		return err
	}
	for _, page := range pageFiles {
		name := page[len("templates/pages/") : len(page)-len(".html")]
		t, err := template.ParseFS(web.Files, "templates/layout.html", "templates/partials/*.html", page)
		if err != nil {
			return fmt.Errorf("parse %s: %w", page, err)
		}
		s.pages[name] = t
	}
	return nil
}

func (s *Server) routes() {
	static, _ := fs.Sub(web.Files, "static")
	s.mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))

	s.mux.HandleFunc("GET /login", s.handleLoginPage)
	s.mux.HandleFunc("POST /login", s.handleLogin)
	s.mux.HandleFunc("GET /2fa", s.handle2FAPage)
	s.mux.HandleFunc("POST /2fa", s.handle2FAVerify)
	s.mux.HandleFunc("POST /logout", s.handleLogout)

	s.mux.Handle("GET /{$}", s.requireAuth(s.handleDashboard))
	s.mux.Handle("GET /settings", s.requireAuth(s.handleSettings))
	s.mux.Handle("POST /settings/password", s.requireAuth(s.handlePasswordChange))
	s.mux.Handle("POST /settings/sessions/clear", s.requireAuth(s.handleSessionsClear))
	s.mux.Handle("POST /settings/registries", s.requireAuth(s.handleRegistryAdd))
	s.mux.Handle("POST /settings/registries/{id}/delete", s.requireAuth(s.handleRegistryDelete))
	s.mux.Handle("POST /settings/integrations", s.requireAuth(s.handleIntegrationsSave))
	s.mux.Handle("POST /settings/2fa/begin", s.requireAuth(s.handle2FASetupBegin))
	s.mux.Handle("POST /settings/2fa/enable", s.requireAuth(s.handle2FAEnable))
	s.mux.Handle("POST /settings/2fa/disable", s.requireAuth(s.handle2FADisable))
	s.mux.Handle("POST /theme", s.requireAuth(s.handleThemeSet))

	s.mux.Handle("GET /logs", s.requireAuth(s.handleLogsPage))
	s.mux.Handle("GET /audit", s.requireAuth(s.handleAuditPage))
	s.mux.Handle("GET /partials/logs", s.requireAuth(s.handleLogsSearchPartial))

	s.mux.Handle("GET /system", s.requireAuth(s.handleSystem))
	s.mux.Handle("POST /system/prune", s.requireAuth(s.handlePrune))
	s.mux.Handle("POST /system/backup", s.requireAuth(s.handleBackupNow))
	s.mux.Handle("GET /system/backups/{name}", s.requireAuth(s.handleBackupDownload))
	s.mux.Handle("POST /system/backups/{name}/delete", s.requireAuth(s.handleBackupDelete))
	s.mux.Handle("POST /system/backups/{name}/restore", s.requireAuth(s.handleBackupRestore))
	s.mux.Handle("POST /system/backup-settings", s.requireAuth(s.handleBackupSettings))
	s.mux.Handle("GET /system/master-key", s.requireAuth(s.handleMasterKeyDownload))
	s.mux.Handle("POST /system/update/check", s.requireAuth(s.handleUpdateCheck))
	s.mux.Handle("POST /system/update/apply", s.requireAuth(s.handleUpdateApply))
	s.mux.Handle("GET /system/containers/{name}", s.requireAuth(s.handleSystemContainerDetail))
	s.mux.Handle("GET /system/containers/{name}/logs", s.requireAuth(s.handleSystemContainerLogs))

	// Deploy webhooks are authenticated by their per-app secret, not a session.
	s.mux.HandleFunc("POST /hooks/{id}/{secret}", s.handleWebhook)
	s.mux.Handle("GET /apps/new", s.requireAuth(s.handleAppNew))
	s.mux.Handle("POST /apps", s.requireAuth(s.handleAppCreate))
	s.mux.Handle("GET /apps/{id}", s.requireAuth(s.handleAppDetail))
	s.mux.Handle("POST /apps/{id}/move", s.requireAuth(s.handleAppMove))
	s.mux.Handle("POST /apps/{id}/start", s.requireAuth(s.appAction("start")))
	s.mux.Handle("POST /apps/{id}/stop", s.requireAuth(s.appAction("stop")))
	s.mux.Handle("POST /apps/{id}/restart", s.requireAuth(s.appAction("restart")))
	s.mux.Handle("POST /apps/{id}/redeploy", s.requireAuth(s.handleAppRedeploy))
	s.mux.Handle("POST /apps/{id}/rollback", s.requireAuth(s.handleAppRollback))
	s.mux.Handle("POST /apps/{id}/delete", s.requireAuth(s.handleAppDelete))
	s.mux.Handle("POST /apps/{id}/env", s.requireAuth(s.handleAppEnvSave))
	s.mux.Handle("POST /apps/{id}/domains", s.requireAuth(s.handleAppDomains))
	s.mux.Handle("POST /apps/{id}/health", s.requireAuth(s.handleAppHealth))
	s.mux.Handle("POST /apps/{id}/pre-backup", s.requireAuth(s.handleAppPreBackup))
	s.mux.Handle("POST /apps/{id}/basic-auth", s.requireAuth(s.handleAppBasicAuth))
	s.mux.Handle("GET /apps/{id}/logs", s.requireAuth(s.handleAppLogs))
	s.mux.Handle("GET /apps/{id}/terminal", s.requireAuth(s.handleTerminalPage))
	s.mux.Handle("GET /apps/{id}/terminal/ws", s.requireAuth(s.handleTerminalWS))
	s.mux.Handle("GET /partials/apps/{id}/tasks", s.requireAuth(s.handleTasksPartial))
	s.mux.Handle("POST /apps/{id}/tasks", s.requireAuth(s.handleTaskAdd))
	s.mux.Handle("POST /apps/{id}/tasks/{task}/run", s.requireAuth(s.handleTaskRun))
	s.mux.Handle("POST /apps/{id}/tasks/{task}/delete", s.requireAuth(s.handleTaskDelete))

	s.mux.Handle("GET /partials/system", s.requireAuth(s.handleSystemPartial))
	s.mux.Handle("GET /partials/system-containers", s.requireAuth(s.handleSystemContainersPartial))
	s.mux.Handle("GET /partials/system/containers/{name}/stats", s.requireAuth(s.handleSystemContainerStatsPartial))
	s.mux.Handle("GET /partials/apps", s.requireAuth(s.handleAppsPartial))
	s.mux.Handle("GET /partials/deploy-fields", s.requireAuth(s.handleDeployFields))
	s.mux.Handle("GET /partials/apps/{id}/stats", s.requireAuth(s.handleAppStatsPartial))
	s.mux.Handle("GET /partials/apps/{id}/status", s.requireAuth(s.handleAppStatusPartial))
	s.mux.Handle("GET /partials/apps/{id}/deployments", s.requireAuth(s.handleAppDeploymentsPartial))
	s.mux.Handle("GET /partials/metrics", s.requireAuth(s.handleServerMetricsPartial))
	s.mux.Handle("GET /partials/apps/{id}/metrics", s.requireAuth(s.handleAppMetricsPartial))
}

func (s *Server) requireAuth(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, _ := r.Cookie(auth.SessionCookie)
		token := ""
		if cookie != nil {
			token = cookie.Value
		}
		if !auth.Valid(s.db, token) {
			// HTMX requests can't follow a 302 to a full page; ask the
			// client to do a real redirect instead.
			if r.Header.Get("HX-Request") == "true" {
				w.Header().Set("HX-Redirect", "/login")
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		next(w, r)
	})
}

// render writes a full page (layout + named page template), injecting the
// active theme from the request cookie.
func (s *Server) render(w http.ResponseWriter, r *http.Request, page string, data map[string]any) {
	t, ok := s.pages[page]
	if !ok {
		http.Error(w, "template not found: "+page, http.StatusInternalServerError)
		return
	}
	if data == nil {
		data = map[string]any{}
	}
	data["Theme"] = themeFrom(r)
	data["Version"] = version.Version
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(w, "layout", data); err != nil {
		log.Printf("render %s: %v", page, err)
	}
}

// renderPartial writes a named partial template without the layout.
func (s *Server) renderPartial(w http.ResponseWriter, name string, data any) {
	// Every page set includes all partials; use the dashboard set as host.
	t := s.pages["dashboard"]
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(w, name, data); err != nil {
		log.Printf("render partial %s: %v", name, err)
	}
}
