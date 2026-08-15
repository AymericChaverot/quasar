package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"quasar/internal/auth"
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

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "dashboard", map[string]any{"Title": "Dashboard", "Domain": s.cfg.Domain})
}

// catalog is the catalogue as this install presents it: the one Quasar ships,
// with the operator's own laid over it.
func (s *Server) catalog() catalog.Catalog {
	return catalog.Builtin().Merge(s.customCatalogs()...)
}

func (s *Server) handleAppNew(w http.ResponseWriter, r *http.Request) {
	cat := s.catalog()
	data := map[string]any{
		"Title":   "New application",
		"Domain":  s.cfg.Domain,
		"Catalog": cat.Grouped(),
	}
	// ?template=<id> prefills the form from a one-click catalog entry, and
	// ?p.NAME=value answers the choices that entry offers — which version of a
	// game server, how much memory, which host port. Carrying them in the query
	// string keeps the selection stateless and shareable: the address of a
	// prefilled form is the whole of what was picked.
	if t := cat.Get(r.URL.Query().Get("template")); t != nil {
		v := t.Resolve(pickedParams(r))
		// The entry proposes an address, but a second server from the same
		// entry would propose the one the first is already on. The public
		// address has to be settled before the env is rendered, because
		// {{URL}} resolves to it.
		sub := s.freeSubdomain(t.SubdomainFor(v))
		f := t.Fill(v, sub, appHost(&db.App{Subdomain: sub}, s.cfg.Domain))
		data["Form"] = &db.App{
			Name:           f.Name,
			Subdomain:      f.Subdomain,
			DeployType:     f.DeployType,
			ImageRef:       f.ImageRef,
			ComposeYAML:    f.Compose,
			ComposeService: f.ComposeService,
			Port:           f.Port,
			DataMount:      f.DataMount,
			EnvContent:     f.Env,
		}
		data["Picked"] = t
		data["Values"] = v
	}
	s.render(w, r, "app_new", data)
}

// pickedParams reads the ?p.NAME=value pairs off the request. Nothing is
// trusted here: Template.Resolve keeps only the parameters the entry declares
// and only the values it accepts, so a hand-edited query string can pick from
// what is offered and nothing else.
func pickedParams(r *http.Request) catalog.Values {
	out := catalog.Values{}
	for k, vs := range r.URL.Query() {
		if name, ok := strings.CutPrefix(k, "p."); ok && len(vs) > 0 {
			out[name] = vs[0]
		}
	}
	return out
}

// freeSubdomain returns want, or the first want-2, want-3… that no application
// holds. One catalogue entry is now meant to be deployed several times over —
// three Minecraft servers on three versions — and every one of them proposing
// the same address would mean renaming all but the first by hand.
func (s *Server) freeSubdomain(want string) string {
	for n := 1; n < 100; n++ {
		try := want
		if n > 1 {
			try = fmt.Sprintf("%s-%d", want, n)
		}
		if taken, err := db.SubdomainTaken(s.db, try); err != nil || !taken {
			return try
		}
	}
	return want
}

func (s *Server) handleAppCreate(w http.ResponseWriter, r *http.Request) {
	form := func(k string) string { return strings.TrimSpace(r.FormValue(k)) }

	a := &db.App{
		Name:           form("name"),
		Subdomain:      strings.ToLower(form("subdomain")),
		DeployType:     form("deploy_type"),
		ImageRef:       form("image_ref"),
		GitURL:         form("git_url"),
		GitBranch:      form("git_branch"),
		ComposeYAML:    r.FormValue("compose_yaml"),
		ComposeService: form("compose_service"),
		EnvContent:     strings.ReplaceAll(r.FormValue("env_content"), "\r\n", "\n"),
		DataMount:      form("data_mount"),
		CustomDomains:  normalizeDomains(form("custom_domains")),
		Port:           80,
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
	// A first deploy has nothing local to reuse: it has to pull the image or
	// clone the repository.
	s.dock.UpdateAsync(a, "create")
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
	// A limit under Docker's own floor is refused by the Engine, so accepting
	// one here would only produce an application whose every deploy fails.
	if a.CPULimit > 0 && a.CPULimit < minCPULimit {
		return fmt.Sprintf("The smallest CPU limit Docker accepts is %g cores.", minCPULimit)
	}
	if a.MemLimitMB > 0 && a.MemLimitMB < minMemLimitMB {
		return fmt.Sprintf("The smallest memory limit Docker accepts is %d MB.", minMemLimitMB)
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
		"App":   s.appDetailView(r, a),
	})
}

// getAppContainer resolves one container of an app's compose project from the
// URL, 404ing on anything that is not part of that project — which is what
// keeps this from becoming a way to read any container on the host.
func (s *Server) getAppContainer(w http.ResponseWriter, r *http.Request, a *db.App) *docker.AppContainer {
	ac, err := s.dock.GetAppContainer(r.Context(), a, r.PathValue("name"))
	if err != nil {
		http.Error(w, "container not found for this application", http.StatusNotFound)
		return nil
	}
	return &ac
}

// handleAppContainerDetail shows one container of a stack: its image, state,
// live resource use and its own logs, rather than the whole project's.
func (s *Server) handleAppContainerDetail(w http.ResponseWriter, r *http.Request) {
	a := s.getApp(w, r)
	if a == nil {
		return
	}
	ac := s.getAppContainer(w, r, a)
	if ac == nil {
		return
	}
	s.render(w, r, "app_container_detail", map[string]any{
		"Title":     ac.Name,
		"App":       s.appView(r, a),
		"Container": ac,
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
		s.renderPartial(w, "app_status_panel", s.appView(r, a))
	}
}

// handleAppRedeploy re-creates the container from what is already on the
// server, which is how a configuration change is applied without also picking
// up whatever has been pushed since.
func (s *Server) handleAppRedeploy(w http.ResponseWriter, r *http.Request) {
	a := s.getApp(w, r)
	if a == nil {
		return
	}
	s.dock.DeployAsync(a, "manual")
	s.audit(r, "app.deploy", a.Name, "")
	s.renderPartial(w, "app_status_panel", s.appView(r, a))
}

// handleAppUpdate goes and gets the newest version first: a git pull and
// rebuild, a fresh image pull, or a compose pull.
func (s *Server) handleAppUpdate(w http.ResponseWriter, r *http.Request) {
	a := s.getApp(w, r)
	if a == nil {
		return
	}
	s.dock.UpdateAsync(a, "update")
	s.audit(r, "app.update", a.Name, a.DeployType)
	s.renderPartial(w, "app_status_panel", s.appView(r, a))
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
	s.renderPartial(w, "app_status_panel", s.appView(r, a))
}

// handleAppTLSRetry makes Traefik ask Let's Encrypt again for the app's
// hostnames, by restarting it.
//
// Traefik requests a certificate when a router first appears; if that attempt
// fails — DNS not propagated yet, port 80 closed, a Let's Encrypt rate limit —
// it does not try again on its own until its configuration changes. Fixing the
// cause therefore does not fix the site, which is why this exists. It restarts
// the shared edge router, so every site is unavailable for a few seconds.
func (s *Server) handleAppTLSRetry(w http.ResponseWriter, r *http.Request) {
	a := s.getApp(w, r)
	if a == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 60*time.Second)
	defer cancel()
	s.audit(r, "tls.retry", a.Name, appHost(a, s.cfg.Domain))
	if err := s.dock.RestartTraefik(ctx); err != nil {
		http.Error(w, "could not restart Traefik: "+err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/apps/"+a.ID, http.StatusSeeOther)
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

// handleAppProtection saves the edge protections Traefik applies in front of
// the app: a per-client rate limit, an address allowlist and browser hardening
// headers.
func (s *Server) handleAppProtection(w http.ResponseWriter, r *http.Request) {
	a := s.getApp(w, r)
	if a == nil {
		return
	}

	rate := 0
	if v := strings.TrimSpace(r.FormValue("rate_limit")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			http.Error(w, "the rate limit must be a positive number of requests per second, or 0", http.StatusBadRequest)
			return
		}
		rate = n
	}

	// Validated here rather than left to Traefik: a malformed CIDR makes
	// Traefik drop the whole middleware, which fails open and would silently
	// leave the app exposed to everyone.
	var cidrs []string
	for _, entry := range strings.Split(r.FormValue("ip_allow_cidrs"), ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		normalized, err := normalizeCIDR(entry)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		cidrs = append(cidrs, normalized)
	}

	headers := r.FormValue("security_headers") == "on"
	if err := db.UpdateAppProtection(s.db, a.ID, rate, strings.Join(cidrs, ","), headers); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.audit(r, "app.protection", a.Name, fmt.Sprintf("rate=%d allow=%q headers=%v", rate, strings.Join(cidrs, ","), headers))
	s.renderPartial(w, "protection_saved", nil)
}

// normalizeCIDR accepts either a bare address or a CIDR block and returns the
// CIDR form Traefik expects, so "1.2.3.4" does not have to be written /32.
func normalizeCIDR(entry string) (string, error) {
	if _, _, err := net.ParseCIDR(entry); err == nil {
		return entry, nil
	}
	ip := net.ParseIP(entry)
	if ip == nil {
		return "", fmt.Errorf("%q is not an IP address or CIDR block (e.g. 203.0.113.4 or 203.0.113.0/24)", entry)
	}
	if ip.To4() != nil {
		return entry + "/32", nil
	}
	return entry + "/128", nil
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

// handleAppGitBuild saves how a git app's checkout is built. It re-renders the
// whole panel rather than a "saved" marker, because the choice changes what the
// panel says about itself.
func (s *Server) handleAppGitBuild(w http.ResponseWriter, r *http.Request) {
	a := s.getApp(w, r)
	if a == nil {
		return
	}
	if a.DeployType != "git" {
		http.Error(w, "only git applications are built from a repository", http.StatusBadRequest)
		return
	}
	mode := r.FormValue("git_build")
	if problem := gitBuildChoiceError(mode, s.dock.GitBuildFor(a)); problem != "" {
		http.Error(w, problem, http.StatusBadRequest)
		return
	}
	if err := db.UpdateAppGitBuild(s.db, a.ID, mode); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	a.GitBuild = mode
	s.audit(r, "app.git-build", a.Name, mode)
	s.renderPartial(w, "git_build_panel", s.appDetailView(r, a))
}

// handleAppComposeService saves which service of a stack the domain is routed
// to. Like the build mode it re-renders its own panel, since the choice changes
// everything the panel reports.
func (s *Server) handleAppComposeService(w http.ResponseWriter, r *http.Request) {
	a := s.getApp(w, r)
	if a == nil {
		return
	}
	if !s.dock.UsesCompose(a) {
		http.Error(w, "only stacks are routed to one of several services", http.StatusBadRequest)
		return
	}
	service := strings.TrimSpace(r.FormValue("compose_service"))
	// Only a service the file actually has: storing anything else would leave
	// every deploy falling back to an unrouted stack, with the panel reporting
	// a service nobody could find.
	if service != "" && !slices.Contains(s.dock.ComposeAdaptationFor(a).Services, service) {
		http.Error(w, "this compose file has no such service", http.StatusBadRequest)
		return
	}
	if err := db.UpdateAppComposeService(s.db, a.ID, service); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.ComposeService = service
	s.audit(r, "app.compose-service", a.Name, service)
	s.renderPartial(w, "compose_route_panel", s.appDetailView(r, a))
}

// gitBuildChoiceError rejects a build mode the checkout cannot honour, and
// returns "" for one it can.
//
// Storing an impossible wish would leave the app permanently disagreeing with
// itself — the panel reporting "Dockerfile", every deploy using compose — and
// the panel hides those options for exactly that reason. This is what stops a
// hand-made request from getting past it. A checkout that is not on disk yet
// constrains nothing: the repository is cloned at the next deploy, and the
// choice is judged then.
func gitBuildChoiceError(mode string, build docker.GitBuild) string {
	if build.Mode == "" {
		return ""
	}
	switch {
	case mode == db.GitBuildDockerfile && !build.HasDockerfile:
		return "this repository has no Dockerfile at its root"
	case mode == db.GitBuildCompose && !build.HasCompose:
		return "this repository has no compose file at its root"
	}
	return ""
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

// Docker's own floors for a limit. Below them the Engine refuses the container
// outright, so a form that accepted 2 MB would only produce a deploy that fails
// later — or, worse, a live change rejected by a daemon the operator never sees.
const (
	minMemLimitMB = 6
	minCPULimit   = 0.01
)

// MinMemLimitMB and MinCPULimit let the form state the floors it enforces.
func (v AppView) MinMemLimitMB() int64 { return minMemLimitMB }
func (v AppView) MinCPULimit() float64 { return minCPULimit }

// handleAppLimits saves the app's CPU and memory ceilings and puts them on the
// running container at once.
//
// It re-renders its own panel rather than a "saved" marker because the answer
// to the save is not "stored" but what the container ended up enforcing: a
// tightened limit applies immediately, a lifted one cannot and leaves a
// redeploy owed, and only the panel can tell the operator which happened.
func (s *Server) handleAppLimits(w http.ResponseWriter, r *http.Request) {
	a := s.getApp(w, r)
	if a == nil {
		return
	}
	cpu, memMB, problem := parseLimits(r)
	if problem != "" {
		http.Error(w, problem, http.StatusBadRequest)
		return
	}
	if err := db.UpdateAppLimits(s.db, a.ID, cpu, memMB); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.CPULimit, a.MemLimitMB = cpu, memMB
	s.audit(r, "app.limits", a.Name, docker.LimitsText(cpu, memMB))

	v := s.appView(r, a)
	state, err := s.dock.ApplyLimits(r.Context(), a)
	v.Limits = state
	if err != nil {
		// Stored but not applied: the panel says so and offers the redeploy
		// that would carry it out. Failing the request would swap nothing in
		// and leave the form claiming the save never happened.
		v.LimitsError = err.Error()
	}
	s.renderPartial(w, "limits_panel", v)
}

// parseLimits reads the two limit fields, returning the problem to report when
// either is not a limit Docker would accept. An empty field means unlimited,
// which is what the placeholder 0 says.
func parseLimits(r *http.Request) (cpu float64, memMB int64, problem string) {
	if v := strings.TrimSpace(r.FormValue("cpu_limit")); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		switch {
		case err != nil || f < 0:
			return 0, 0, "the CPU limit must be a number of cores, or 0 for unlimited"
		case f > 0 && f < minCPULimit:
			return 0, 0, fmt.Sprintf("the smallest CPU limit Docker accepts is %g cores", minCPULimit)
		}
		cpu = f
	}
	if v := strings.TrimSpace(r.FormValue("mem_limit_mb")); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		switch {
		case err != nil || n < 0:
			return 0, 0, "the memory limit must be a whole number of MB, or 0 for unlimited"
		case n > 0 && n < minMemLimitMB:
			return 0, 0, fmt.Sprintf("the smallest memory limit Docker accepts is %d MB", minMemLimitMB)
		}
		memMB = n
	}
	return cpu, memMB, ""
}

// handleAppBasicAuth enables/disables Traefik basic auth (applied at redeploy).
//
// Like the build mode it re-renders its own panel rather than a "saved" marker:
// the password itself can never be shown back, so the panel reporting who is
// protected and whether the running container agrees is the only evidence an
// operator has that the save landed.
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
		a.BasicAuthUser, a.BasicAuthHash = "", ""
		s.audit(r, "app.basic-auth", a.Name, "disabled")
		s.renderPartial(w, "basic_auth_panel", s.appDetailView(r, a))
		return
	}
	user := strings.TrimSpace(r.FormValue("ba_user"))
	pass := r.FormValue("ba_password")
	if user == "" || len(pass) < basicAuthMinLength {
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
	a.BasicAuthUser, a.BasicAuthHash = user, string(hash)
	s.audit(r, "app.basic-auth", a.Name, user)
	s.renderPartial(w, "basic_auth_panel", s.appDetailView(r, a))
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
