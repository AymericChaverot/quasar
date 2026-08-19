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
	"quasar/internal/db"
	"quasar/internal/docker"
	"quasar/internal/files"
	"quasar/internal/secrets"
	"quasar/internal/station/ui"
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

	// traefik is the edge-router update in flight, for the same reason and read
	// the same way — by the Environment card, which polls while one is running.
	traefik traefikRun

	// jobs are the long station actions running now, and the ones somebody may
	// still come back to read.
	jobs stationJobRegistry
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
// hard; nothing else here can, since the stylesheets change under fixed names.
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
	// The event a station's panel is re-fetched by. Kept as a function so the
	// name the template listens for and the name the action's response sends
	// cannot drift apart.
	"stationRefresh":    stationRefreshEvent,
	"stationRefreshAll": stationRefreshAllEvent,
	"stationConfirm":    ui.Interpolate,
	// The sort of thing a filename suggests, which the listing turns into an
	// icon so one row can be told from the next at a glance.
	"fileKind": files.Kind,
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
	// An operator's catalogue is a set of compose files this server will run,
	// so writing one is an admin's business — including the read, which shows
	// the documents in full.
	s.admin("GET /settings/catalogs", s.handleCatalogs)
	s.admin("POST /settings/catalogs", s.handleCatalogCreate)
	s.admin("POST /settings/catalogs/fetch", s.handleCatalogFetch)
	s.admin("POST /settings/catalogs/{id}", s.handleCatalogUpdate)
	s.admin("POST /settings/catalogs/{id}/fetch", s.handleCatalogFetch)
	s.admin("POST /settings/catalogs/{id}/toggle", s.handleCatalogToggle)
	s.admin("POST /settings/catalogs/{id}/delete", s.handleCatalogDelete)
	// The same document, edited an entry at a time rather than as text.
	s.admin("POST /settings/catalogs/start", s.handleCatalogStart)
	s.admin("GET /settings/catalogs/{id}/entries/{entry}", s.handleCatalogEntryForm)
	s.admin("POST /settings/catalogs/{id}/entries/{entry}", s.handleCatalogEntrySave)
	s.admin("POST /settings/catalogs/{id}/entries/{entry}/delete", s.handleCatalogEntryDelete)
	// The stations an operator can deploy from, on a page of their own rather
	// than mixed into the catalogue: one is software to browse, the other is a
	// program somebody wrote for you.
	s.viewer("GET /stations", s.handleStationsPage)
	// Starring one lifts it to its own list at the top. The star belongs to the
	// install rather than to whoever is looking, so setting it is an admin's.
	s.admin("POST /stations/{id}/favorite", s.handleStationFavorite)
	// Installing a station is its own page, not the new-application form with
	// a station in it: somebody else has already answered everything that form
	// asks, and the only questions left are the station's own.
	s.admin("GET /stations/{id}/deploy", s.handleStationDeployForm)
	// And the last button is not the form's submit. Between the two is a recap
	// of what is about to exist and where, with the way back to the answers
	// that decided it: installing takes an address on the operator's domain and
	// starts pulling an image, which is worth a dozen words of warning.
	s.admin("POST /stations/{id}/deploy/review", s.handleStationDeployReview)
	s.admin("POST /stations/{id}/deploy/edit", s.handleStationDeployEdit)
	s.admin("POST /stations/{id}/deploy", s.handleStationDeploy)
	// A station is a program somebody else wrote, and installing one is
	// accepting what it may reach. Reading the page is an admin's business for
	// the same reason writing a catalogue is.
	s.admin("GET /settings/stations", s.handleStations)
	s.admin("POST /settings/stations", s.handleStationInstall)
	s.admin("POST /settings/stations/review", s.handleStationReview)
	s.admin("POST /settings/stations/fetch", s.handleStationFetch)
	s.admin("POST /settings/stations/{id}/fetch", s.handleStationRefetch)
	s.admin("POST /settings/stations/{id}/accept", s.handleStationAccept)
	s.admin("POST /settings/stations/{id}/discard", s.handleStationDiscard)
	s.admin("POST /settings/stations/{id}/revert", s.handleStationRevert)
	// A station's own panels and actions. Admin-only, because fetching a panel
	// runs somebody else's script with whatever the document was granted —
	// which is not a read, whatever it looks like on the page.
	s.admin("GET /apps/{id}/station/panel/{panel}", s.handleStationPanelPartial)
	// An embedded page is somebody else's application, proxied onto this one:
	// it makes whatever requests it makes, so this takes every method. Where it
	// goes is read out of the document and never out of the URL.
	s.admin("/apps/{id}/station/embed/{panel}/{path...}", s.handleStationEmbed)
	s.admin("POST /apps/{id}/station/action/{action}", s.handleStationAction)
	s.admin("GET /apps/{id}/station/job/{action}", s.handleStationJob)
	s.admin("POST /settings/stations/{id}/toggle", s.handleStationToggle)
	s.admin("POST /settings/stations/{id}/delete", s.handleStationDelete)
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
	// Recreating the edge router stops every site for a few seconds, this page
	// included.
	s.admin("POST /system/traefik/update", s.handleTraefikUpdate)
	// The page that waits out an update, and what it polls. Admin-only like the
	// update itself: nobody else can start one, so nobody else is waiting.
	s.admin("GET /system/updating", s.handleUpdating)
	s.admin("GET /system/update/status", s.handleUpdateStatus)

	// The storage explorer, over an app's mounts and over any Docker volume.
	// Admin-only: what it opens is application data — databases, uploads, the
	// secrets some images write to a config file — which is the same material
	// the backup archive holds, and that is admin-only too.
	s.admin("GET /files/{kind}/{ref}", s.handleFiles)
	s.admin("GET /partials/files/{kind}/{ref}", s.handleFilesPartial)
	// Writing is only ever offered where the filesystem takes it — an app's own
	// data directory, and a Docker volume on an install that has mounted the
	// volume tree in. Each of these refuses on its own rather than relying on
	// the button having been hidden.
	s.admin("POST /files/{kind}/{ref}/upload", s.handleFilesUpload)
	s.admin("POST /files/{kind}/{ref}/save", s.handleFilesSave)
	s.admin("POST /files/{kind}/{ref}/delete", s.handleFilesDelete)

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
	// The build's own output, which carries whatever the Dockerfile echoed and
	// whatever compose interpolated — admin-only for the same reason the
	// environment editor on the same page is.
	s.admin("GET /apps/{id}/deploy-log", s.handleAppDeployLog)
	s.admin("POST /apps/{id}/delete", s.handleAppDelete)
	s.admin("POST /apps/{id}/env", s.handleAppEnvSave)
	s.admin("POST /apps/{id}/domains", s.handleAppDomains)
	s.admin("POST /apps/{id}/git-build", s.handleAppGitBuild)
	s.admin("POST /apps/{id}/compose-service", s.handleAppComposeService)
	s.admin("POST /apps/{id}/health", s.handleAppHealth)
	// Applied to the running container, not merely stored for the next deploy.
	s.admin("POST /apps/{id}/limits", s.handleAppLimits)
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
	// The four sections of the System page that cost real time to produce. They
	// are fetched once the page is on screen rather than before it, and they run
	// concurrently because the browser asks for all four at once.
	s.viewer("GET /partials/system/environment", s.handleSystemEnvPartial)
	s.viewer("GET /partials/system/certs", s.handleSystemCertsPartial)
	s.viewer("GET /partials/system/storage", s.handleSystemStoragePartial)
	s.viewer("GET /partials/system/app-sizes", s.handleSystemAppSizesPartial)
	// Admin-only like the explorer it leads into: the list names every volume on
	// the server and links straight into its contents.
	s.admin("GET /partials/system/volumes", s.handleSystemVolumesPartial)
	s.admin("GET /partials/apps/{id}/storage", s.handleAppStoragePartial)
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
	// Admin-only like the panel it refreshes, which sits inside the form that
	// sets the limits it reports.
	s.admin("GET /partials/apps/{id}/limits", s.handleAppLimitsPartial)
	// Admin-only like the button it refreshes: only an admin is offered the
	// update, so only an admin's header polls for one.
	s.admin("GET /partials/update-badge", s.handleUpdateBadgePartial)
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
	// The Stations entry only appears once there is a station to reach from
	// it. An install with none has no business carrying a navigation entry for
	// a page that would be empty.
	data["HasStations"] = db.CountEnabledStations(s.db) > 0
	// Injected for every page so templates can hide controls a viewer would
	// only get a 403 from. requireAdmin is what actually enforces it; this is
	// presentation.
	_, _, role, _ := s.currentUser(r)
	data["Role"] = role
	data["IsAdmin"] = role == auth.RoleAdmin
	// The header's update button, on every page for the same reason the version
	// is: a release waiting to be installed should be visible wherever you
	// happen to be, not only on the System page. Only an admin can apply one,
	// so only an admin is shown it.
	//
	// Kept under its own key rather than merged into the page's data: the
	// System page already carries UpdateAvail and Latest for the update card,
	// and two things writing the same keys would have the header and the card
	// silently fighting over them.
	hideNav, _ := data["HideNav"].(bool)
	if role == auth.RoleAdmin && !hideNav {
		data["Update"] = s.updateBadgeData(true)
	}
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
	case strings.HasPrefix(path, "/stations"):
		return "stations"
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
