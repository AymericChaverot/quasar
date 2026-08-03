package server

import (
	"database/sql"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"strings"

	"quasar/internal/auth"
	"quasar/internal/config"
	"quasar/internal/docker"
	"quasar/internal/secrets"
	"quasar/internal/version"
	"quasar/web"
)

// Access levels a route can be registered at.
const (
	accessPublic = "public" // no session needed
	accessSelf   = "self"   // any signed-in user, including a viewer
	accessAdmin  = "admin"  // admins only

	// The JSON API authenticates with a Bearer token instead of a session, but
	// enforces the same two roles.
	accessTokenRead  = "token-read"
	accessTokenAdmin = "token-admin"
)

type Server struct {
	cfg     config.Config
	db      *sql.DB
	dock    *docker.Client
	keyring *secrets.Keyring
	pages   map[string]*template.Template
	mux     *http.ServeMux

	// guards records the access level every route was registered at, so the
	// route table can be asserted on rather than trusted. Adding a mutating
	// route without admin gating is the mistake this makes visible.
	guards map[string]string

	// edgeAttempts caps how fast one address may guess at one application's
	// password on the public login page.
	edgeAttempts *edgeThrottle

	// update is the self-update in flight, if any: the pull runs detached from
	// the request that asked for it, and this is where the page waiting on it
	// reads how far it has got.
	update updateRun
}

func New(cfg config.Config, database *sql.DB, dock *docker.Client, keyring *secrets.Keyring) (*Server, error) {
	s := &Server{
		cfg:     cfg,
		db:      database,
		dock:    dock,
		keyring: keyring,
		pages:   map[string]*template.Template{},
		mux:     http.NewServeMux(),
		guards:  map[string]string{},

		edgeAttempts: newEdgeThrottle(),
	}
	if err := s.parseTemplates(); err != nil {
		return nil, err
	}
	s.routes()
	return s, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

// parseTemplates builds one template set per page: layout + partials + page.
// staticAssets serves the embedded static tree.
//
// Two things the bare file server does not do for us. Go's built-in MIME table
// has no entry for .woff2, and the container image carries no /etc/mime.types
// to fall back on, so the fonts would go out sniffed as octet-stream. And
// files read out of an embed.FS have a zero modification time, so there is no
// Last-Modified to revalidate against and every page load would pull the fonts
// down again. Their names are tied to their contents, so they can be pinned
// hard; nothing else here can, since themes.css changes under a fixed name.
func staticAssets(root fs.FS) http.Handler {
	files := http.FileServerFS(root)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".woff2") {
			w.Header().Set("Content-Type", "font/woff2")
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		files.ServeHTTP(w, r)
	})
}

// templateFuncs are the helpers pages and the layout may call.
var templateFuncs = template.FuncMap{
	// navActive returns the class marking a nav entry as the current section.
	// It takes Nav as any and tolerates it being absent so a page can still be
	// rendered from a partial data set — the template tests do exactly that,
	// and a missing key would otherwise fail the comparison at execution time.
	"navActive": func(nav any, section string) string {
		if s, ok := nav.(string); ok && s == section {
			return "is-active"
		}
		return ""
	},
	// Byte counts are shown in whichever unit the number actually fills, so a
	// 40 MB layer and a 4 GB one are not both reported as "0.0 GB".
	"humanSize": docker.HumanSize,
	// plural picks the word that agrees with a count, so the interface reads as
	// prose instead of as "1 object(s)".
	"plural": func(n int, one, many string) string {
		if n == 1 {
			return one
		}
		return many
	},
	"hasPrefix": strings.HasPrefix,
	// dict builds the argument for a partial that needs more than one value —
	// a template can only be passed a single dot, and the alternative is
	// copying the partial's markup once per call site.
	"dict": func(pairs ...any) (map[string]any, error) {
		if len(pairs)%2 != 0 {
			return nil, fmt.Errorf("dict: odd number of arguments")
		}
		m := make(map[string]any, len(pairs)/2)
		for i := 0; i < len(pairs); i += 2 {
			key, ok := pairs[i].(string)
			if !ok {
				return nil, fmt.Errorf("dict: key %v is not a string", pairs[i])
			}
			m[key] = pairs[i+1]
		}
		return m, nil
	},
}

func (s *Server) parseTemplates() error {
	pageFiles, err := fs.Glob(web.Files, "templates/pages/*.html")
	if err != nil {
		return err
	}
	for _, page := range pageFiles {
		name := page[len("templates/pages/") : len(page)-len(".html")]
		t, err := template.New("layout.html").Funcs(templateFuncs).
			ParseFS(web.Files, "templates/layout.html", "templates/partials/*.html", page)
		if err != nil {
			return fmt.Errorf("parse %s: %w", page, err)
		}
		s.pages[name] = t
	}
	return nil
}

// public registers a route reachable without a session.
func (s *Server) public(pattern string, h http.HandlerFunc) {
	s.guards[pattern] = accessPublic
	s.mux.HandleFunc(pattern, h)
}

// viewer registers a route any signed-in user may reach, viewers included.
// Only for reads and for actions on one's own account.
func (s *Server) viewer(pattern string, h http.HandlerFunc) {
	s.guards[pattern] = accessSelf
	s.mux.Handle(pattern, s.requireAuth(h))
}

// admin registers a route that changes the platform, or that hands over power
// equivalent to changing it.
func (s *Server) admin(pattern string, h http.HandlerFunc) {
	s.guards[pattern] = accessAdmin
	s.mux.Handle(pattern, s.requireAdmin(h))
}

// apiRead registers a JSON route any valid token may call.
func (s *Server) apiRead(pattern string, h http.HandlerFunc) {
	s.guards[pattern] = accessTokenRead
	s.mux.Handle(pattern, s.requireToken(false, h))
}

// apiWrite registers a JSON route that needs a token issued as admin.
func (s *Server) apiWrite(pattern string, h http.HandlerFunc) {
	s.guards[pattern] = accessTokenAdmin
	s.mux.Handle(pattern, s.requireToken(true, h))
}

func (s *Server) routes() {
	static, _ := fs.Sub(web.Files, "static")
	s.mux.Handle("GET /static/", http.StripPrefix("/static/", staticAssets(static)))
	s.guards["GET /static/"] = accessPublic

	s.public("GET /login", s.handleLoginPage)
	s.public("POST /login", s.handleLogin)
	s.public("GET /2fa", s.handle2FAPage)
	s.public("POST /2fa", s.handle2FAVerify)
	s.public("POST /logout", s.handleLogout)
	// Deploy webhooks are authenticated by their per-app secret, not a session.
	s.public("POST /hooks/{id}/{secret}", s.handleWebhook)
	// Called by Traefik, not by a person, before every request to a
	// password-protected application. It authorises against that application's
	// own credentials, so a dashboard session is neither required nor useful.
	s.public("GET /edge-auth/{id}", s.handleEdgeAuth)

	s.viewer("GET /{$}", s.handleDashboard)
	s.viewer("GET /settings", s.handleSettings)
	s.viewer("POST /theme", s.handleThemeSet)
	// Own-account actions: a viewer has to be able to secure their own login.
	s.viewer("POST /settings/password", s.handlePasswordChange)
	s.viewer("POST /settings/2fa/begin", s.handle2FASetupBegin)
	s.viewer("POST /settings/2fa/enable", s.handle2FAEnable)
	s.viewer("POST /settings/2fa/disable", s.handle2FADisable)

	s.admin("POST /settings/sessions/clear", s.handleSessionsClear)
	s.admin("POST /settings/registries", s.handleRegistryAdd)
	s.admin("POST /settings/registries/{id}/delete", s.handleRegistryDelete)
	s.admin("POST /settings/integrations", s.handleIntegrationsSave)
	// Forge tokens open every private repository the platform builds from, so
	// the whole section is admin-only — including the read that lists which
	// hosts have one.
	s.admin("GET /settings/git", s.handleGitCredentials)
	s.admin("POST /settings/git", s.handleGitCredentialSave)
	s.admin("POST /settings/git/{id}/update", s.handleGitCredentialUpdate)
	s.admin("POST /settings/git/{id}/delete", s.handleGitCredentialDelete)
	s.admin("POST /settings/git/test", s.handleGitCredentialTest)
	s.admin("POST /settings/notify-test", s.handleNotifyTest)
	s.admin("POST /settings/users", s.handleUserCreate)
	s.admin("POST /settings/users/{id}/role", s.handleUserRole)
	s.admin("POST /settings/users/{id}/password", s.handleUserPassword)
	s.admin("POST /settings/users/{id}/delete", s.handleUserDelete)
	s.admin("POST /settings/tokens", s.handleTokenCreate)
	s.admin("POST /settings/tokens/{id}/delete", s.handleTokenDelete)

	// JSON API, authenticated by Bearer token rather than a session.
	s.apiRead("GET /api/v1/apps", s.handleAPIApps)
	s.apiRead("GET /api/v1/apps/{id}", s.handleAPIApp)
	s.apiRead("GET /api/v1/system", s.handleAPISystem)
	s.apiWrite("POST /api/v1/apps/{id}/deploy", s.handleAPIDeploy)
	s.apiWrite("POST /api/v1/apps/{id}/restart", s.handleAPIRestart)

	s.viewer("GET /logs", s.handleLogsPage)
	s.viewer("GET /audit", s.handleAuditPage)
	s.viewer("GET /partials/logs", s.handleLogsSearchPartial)

	s.viewer("GET /system", s.handleSystem)
	s.viewer("GET /system/containers/{name}", s.handleSystemContainerDetail)
	s.viewer("GET /system/containers/{name}/logs", s.handleSystemContainerLogs)
	s.admin("POST /system/cleanup", s.handleCleanup)
	s.admin("POST /system/backup", s.handleBackupNow)
	// An archive contains every app's data and .env: a read, but not one a
	// viewer gets.
	s.admin("GET /system/backups/{name}", s.handleBackupDownload)
	s.admin("POST /system/backups/{name}/delete", s.handleBackupDelete)
	s.admin("POST /system/backups/{name}/restore", s.handleBackupRestore)
	s.admin("POST /system/backup-settings", s.handleBackupSettings)
	s.admin("POST /system/offsite-settings", s.handleOffsiteSettings)
	s.admin("POST /system/offsite-test", s.handleOffsiteTest)
	// Opens every encrypted value on the platform.
	s.admin("GET /system/master-key", s.handleMasterKeyDownload)
	// Deleting a certificate restarts Traefik, so it briefly stops every site.
	s.admin("POST /system/certs/{domain}/delete", s.handleCertDelete)
	s.admin("POST /system/update/check", s.handleUpdateCheck)
	s.admin("POST /system/update/apply", s.handleUpdateApply)
	// The page that waits out an update, and what it polls. Admin-only like the
	// update itself: nobody else can start one, so nobody else is waiting.
	s.admin("GET /system/updating", s.handleUpdating)
	s.admin("GET /system/update/status", s.handleUpdateStatus)

	s.viewer("GET /apps/{id}", s.handleAppDetail)
	s.viewer("GET /apps/{id}/logs", s.handleAppLogs)
	// Per-container views of a compose stack. {name} is resolved against the
	// app's own project, so it cannot reach another app's containers.
	s.viewer("GET /apps/{id}/containers/{name}", s.handleAppContainerDetail)
	s.viewer("GET /apps/{id}/containers/{name}/logs", s.handleAppContainerLogs)
	s.admin("GET /apps/new", s.handleAppNew)
	s.admin("POST /apps", s.handleAppCreate)
	s.admin("POST /apps/{id}/move", s.handleAppMove)
	s.admin("POST /apps/{id}/start", s.appAction("start"))
	s.admin("POST /apps/{id}/stop", s.appAction("stop"))
	s.admin("POST /apps/{id}/restart", s.appAction("restart"))
	s.admin("POST /apps/{id}/redeploy", s.handleAppRedeploy)
	s.admin("POST /apps/{id}/update", s.handleAppUpdate)
	s.admin("POST /apps/{id}/rollback", s.handleAppRollback)
	s.admin("POST /apps/{id}/delete", s.handleAppDelete)
	s.admin("POST /apps/{id}/env", s.handleAppEnvSave)
	s.admin("POST /apps/{id}/domains", s.handleAppDomains)
	s.admin("POST /apps/{id}/git-build", s.handleAppGitBuild)
	s.admin("POST /apps/{id}/compose-service", s.handleAppComposeService)
	s.admin("POST /apps/{id}/health", s.handleAppHealth)
	s.admin("POST /apps/{id}/pre-backup", s.handleAppPreBackup)
	s.admin("POST /apps/{id}/protection", s.handleAppProtection)
	s.admin("POST /apps/{id}/basic-auth", s.handleAppBasicAuth)
	// Restarts the shared edge router, briefly taking every site down.
	s.admin("POST /apps/{id}/tls/retry", s.handleAppTLSRetry)
	// A shell in the container reads every secret the app has and can change
	// anything, so it is an admin action however read-only the HTTP verb looks.
	s.admin("GET /apps/{id}/terminal", s.handleTerminalPage)
	s.admin("GET /apps/{id}/terminal/ws", s.handleTerminalWS)
	s.viewer("GET /partials/apps/{id}/tasks", s.handleTasksPartial)
	s.admin("POST /apps/{id}/tasks", s.handleTaskAdd)
	// Runs an arbitrary command in the container.
	s.admin("POST /apps/{id}/tasks/{task}/run", s.handleTaskRun)
	s.admin("POST /apps/{id}/tasks/{task}/delete", s.handleTaskDelete)

	s.viewer("GET /partials/system", s.handleSystemPartial)
	s.viewer("GET /partials/system-containers", s.handleSystemContainersPartial)
	s.viewer("GET /partials/system/containers/{name}/stats", s.handleSystemContainerStatsPartial)
	s.viewer("GET /partials/apps", s.handleAppsPartial)
	s.viewer("GET /partials/deploy-fields", s.handleDeployFields)
	s.viewer("GET /partials/apps/{id}/stats", s.handleAppStatsPartial)
	s.viewer("GET /partials/apps/{id}/status", s.handleAppStatusPartial)
	s.viewer("GET /partials/apps/{id}/containers", s.handleAppContainersPartial)
	s.viewer("GET /partials/apps/{id}/containers/{name}/stats", s.handleAppContainerStatsPartial)
	s.viewer("GET /partials/apps/{id}/deployments", s.handleAppDeploymentsPartial)
	s.viewer("GET /partials/apps/{id}/tls", s.handleAppTLSPartial)
	// Admin-only like the panel it refreshes: it names the account the app is
	// protected with, which a viewer is not shown.
	s.admin("GET /partials/apps/{id}/basic-auth", s.handleAppBasicAuthPartial)
	s.viewer("GET /partials/metrics", s.handleServerMetricsPartial)
	s.viewer("GET /partials/apps/{id}/metrics", s.handleAppMetricsPartial)
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

// requireAdmin authenticates and then refuses viewers. The refusal is a plain
// 403 rather than a redirect: the user is signed in, so sending them to the
// login page would suggest their session expired.
func (s *Server) requireAdmin(next http.HandlerFunc) http.Handler {
	return s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		if _, _, role, _ := s.currentUser(r); role != auth.RoleAdmin {
			http.Error(w, "This account has read-only access.", http.StatusForbidden)
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
	data["Nav"] = navSection(r.URL.Path)
	// Injected for every page so templates can hide controls a viewer would
	// only get a 403 from. requireAdmin is what actually enforces it; this is
	// presentation.
	_, _, role, _ := s.currentUser(r)
	data["Role"] = role
	data["IsAdmin"] = role == auth.RoleAdmin
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(w, "layout", data); err != nil {
		log.Printf("render %s: %v", page, err)
	}
}

// navSection maps a request path to the header entry that should read as the
// current one. Pages nested under a section — an application, one of its
// containers, its terminal — keep that section lit rather than leaving the
// header blank, which would suggest you had navigated out of it.
func navSection(path string) string {
	switch {
	case path == "/apps/new":
		return "new"
	case path == "/" || strings.HasPrefix(path, "/apps/"):
		return "apps"
	case strings.HasPrefix(path, "/logs"):
		return "logs"
	case strings.HasPrefix(path, "/audit"):
		return "audit"
	case strings.HasPrefix(path, "/system"):
		return "system"
	case strings.HasPrefix(path, "/settings"):
		return "settings"
	}
	return ""
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
