package server

import (
	"fmt"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/crypto/bcrypt"

	"quasar/internal/db"
	"quasar/internal/docker"
)

// The forms on an application's page, one per thing about it that can be
// changed after it exists: edge protections, a pre-backup command, how it is
// built from Git, which compose service is the web one, the health path, the
// resource ceilings, the password in front of it, its domains, and its
// environment.
//
// Each saves one thing and re-renders its own panel, rather than the page: the
// answer to a save here is usually what the running container ended up with,
// which only that panel is in a position to say.

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
			if err := db.UpdateAppCompose(s.db, s.keyring, a.ID, yaml); err != nil {
				http.Error(w, "database error: "+err.Error(), http.StatusInternalServerError)
				return
			}
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
