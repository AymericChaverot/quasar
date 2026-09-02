package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"quasar/internal/catalog"
	"quasar/internal/db"
	"quasar/internal/docker"
)

// Applications: listing them, creating one, and the actions that move an
// existing one — start, stop, redeploy, update, roll back, reorder, delete.
//
// The forms on an application's own page, which change one thing about it
// rather than acting on it, are in handlers_app_settings.go.

var (
	subdomainRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	domainRe    = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,}$`)
)

func randomHex(n int) string {
	buf := make([]byte, n)
	rand.Read(buf)
	return hex.EncodeToString(buf)
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "dashboard", map[string]any{
		"Title": "Dashboard", "Domain": s.cfg.Domain,
		// What the history picker has to offer, from the same list the
		// partial behind it reads, so the two cannot drift apart.
		"Measurements": picker(r, ServerPicker, ServerMeasurements),
	})
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
		// The operator's catalogues, named, so the page can offer each of them
		// as its own way in rather than pouring them all into one long list
		// with the sixty entries Quasar ships. Somebody who wrote a catalogue
		// of their own came here to pick from that one.
		"Sources": s.customCatalogs(),
	}
	// ?template=<id> prefills the form from a one-click catalog entry, and
	// ?p.NAME=value answers the choices that entry offers — which version of a
	// game server, how much memory, which host port. Carrying them in the query
	// string keeps the selection stateless and shareable: the address of a
	// prefilled form is the whole of what was picked.
	//
	// A station does not come through here. It has an install page of its own,
	// because installing one is a different thing from filling in this form and
	// should not have to look like it.
	if t := cat.Get(r.URL.Query().Get("template")); t != nil {
		v := t.Resolve(pickedParams(r.URL.Query()))
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

// pickedParams reads the p.NAME=value pairs out of a query string or a posted
// form. Nothing is trusted here: Template.Resolve keeps only the parameters the
// entry declares and only the values it accepts, so a hand-edited request can
// pick from what is offered and nothing else.
func pickedParams(values url.Values) catalog.Values {
	out := catalog.Values{}
	for k, vs := range values {
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

	if errMsg := s.createApp(w, r, a); errMsg != "" {
		s.render(w, r, "app_new", map[string]any{
			"Title": "New application", "Domain": s.cfg.Domain,
			"Error": errMsg, "Form": a,
		})
	}
}

// createApp checks, stores and deploys a new application, and returns what is
// wrong with it — empty when it was created, in which case the redirect has
// already been written.
//
// It is separate from the form that fills it in because there are now two of
// those: the general one, and a station's own. What happens after the fields
// are known should not depend on which page they came from.
func (s *Server) createApp(w http.ResponseWriter, r *http.Request, a *db.App) string {
	if a.GitBranch == "" {
		a.GitBranch = "main"
	}
	if errMsg := s.validateNewApp(a); errMsg != "" {
		return errMsg
	}

	a.ID = randomHex(4)
	a.WebhookSecret = randomHex(16)

	if err := db.InsertApp(s.db, s.keyring, a); err != nil {
		return "database error: " + err.Error()
	}
	// A first deploy has nothing local to reuse: it has to pull the image or
	// clone the repository.
	s.dock.UpdateAsync(a, "create")
	s.audit(r, "app.create", a.Name, a.Subdomain+" ("+a.DeployType+")")
	http.Redirect(w, r, "/apps/"+a.ID, http.StatusSeeOther)
	return ""
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
		if msg := s.portConflict(a); msg != "" {
			return msg
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

// portConflict reports a host port this stack would bind that another
// application already binds, naming both.
//
// Only the servers that do not speak HTTP ever reach this: a web app is routed
// by Host header and binds nothing on the host, and Quasar's compose adaptation
// drops the bindings on 80 and 443 that Traefik holds. What is left is the game
// servers and the databases, which are exactly the entries somebody deploys
// several of. Two Minecraft servers on 25565 is a stack that comes up, fails to
// bind, and stops — with the reason in a log nobody is watching, since the
// dashboard would have shown the deploy as done.
func (s *Server) portConflict(a *db.App) string {
	wanted := docker.PublishedPorts(a.ComposeYAML, docker.EnvMap(a.EnvContent))
	if len(wanted) == 0 {
		return ""
	}
	apps, err := db.ListApps(s.db, s.keyring)
	if err != nil {
		return ""
	}
	held := map[int]string{}
	for _, other := range apps {
		if other.ID == a.ID || other.DeployType != "compose" {
			continue
		}
		for _, p := range docker.PublishedPorts(other.ComposeYAML, docker.EnvMap(other.EnvContent)) {
			held[p] = other.Name
		}
	}
	for _, p := range wanted {
		if name, ok := held[p]; ok {
			return fmt.Sprintf("Port %d is already published by %q. Two containers cannot bind the same host port — "+
				"give this one a different port, in the compose file or in the variable it reads it from.", p, name)
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
	data := map[string]any{
		"Title": a.Name,
		"App":   s.appDetailView(r, a),
		// What this page's history picker has to offer. An application keeps
		// two of the three the server does: its disk is not sampled a minute
		// at a time.
		"Measurements": picker(r, AppPicker, AppMeasurements),
		// The storage tab keeps its own history, and its own picker with it.
		"StorageMeasurements": picker(r, StoragePicker, StorageMeasurements),
	}
	// A station renders as one block at the top of this page, and only for an
	// admin: fetching a panel runs somebody else's script with whatever the
	// document was granted, which is not a read.
	if s.isAdmin(r) {
		if block := s.stationBlock(a); block != nil {
			data["Station"] = block
		}
	}
	s.render(w, r, "app_detail", data)
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

// handleAppRollback puts the application back on an earlier deployment.
//
// What "earlier deployment" means depends on the shape. A single container goes
// back to an image tag; a stack goes back to the compose file that deployment
// ran, because there is no one tag that describes several services. Both are
// posted from the same button and both are checked against this application's
// own history before anything is deployed — the value arrives from a form, and
// a tag or an id from somewhere else must not be a way to run something.
func (s *Server) handleAppRollback(w http.ResponseWriter, r *http.Request) {
	a := s.getApp(w, r)
	if a == nil {
		return
	}
	deps, _ := db.ListDeployments(s.db, a.ID, 50)

	if id := r.FormValue("deployment"); id != "" {
		depID, err := strconv.ParseInt(id, 10, 64)
		if err != nil {
			http.Error(w, "not a deployment of this application", http.StatusBadRequest)
			return
		}
		if !slices.ContainsFunc(deps, func(d *db.Deployment) bool {
			return d.ID == depID && d.Status == "success" && d.HasCompose
		}) {
			http.Error(w, "not a deployment of this application", http.StatusBadRequest)
			return
		}
		s.dock.RollbackComposeAsync(a, depID)
		s.audit(r, "app.rollback", a.Name, "to the compose file of deployment "+id)
		s.renderPartial(w, "app_status_panel", s.appView(r, a))
		return
	}

	tag := r.FormValue("tag")
	// Only tags recorded in this app's history are accepted.
	if !slices.ContainsFunc(deps, func(d *db.Deployment) bool {
		return d.ImageTag != "" && d.ImageTag == tag && d.Status == "success"
	}) {
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
		if err := db.SetAppOrder(s.db, other.ID, i+1); err != nil {
			http.Error(w, "database error: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	s.handleAppsPartial(w, r)
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
	// The app row is already gone. What is left is its debris, and a failure
	// to clear it leaves orphaned rows rather than a half-deleted app — worth
	// a log line, not worth telling the operator the delete failed when it did
	// not.
	for what, err := range map[string]error{
		"deployments":   db.DeleteDeployments(s.db, a.ID),
		"tasks":         db.DeleteTasksForApp(s.db, a.ID),
		"time series":   db.DeleteAppTimeSeries(s.db, a.ID),
		"stored output": db.DeleteAppLogs(s.db, a.ID),
	} {
		if err != nil {
			log.Printf("app delete: clearing the %s of %s: %v", what, a.ID, err)
		}
	}
	// Recorded after the fact and deliberately outside DeleteAppLogs' reach:
	// deleting an app must not also erase the record of who deleted it.
	s.audit(r, "app.delete", a.Name, a.Subdomain+" ("+a.DeployType+")")
	w.Header().Set("HX-Redirect", "/")
	w.WriteHeader(http.StatusOK)
}
