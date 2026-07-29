package server

import (
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"quasar/internal/db"
)

// GitCredentialView pairs a stored credential with the applications it would
// actually authenticate.
//
// Host matching is invisible otherwise: an operator who typed "github.com"
// into the host field has no way to check that it is what their repository
// URLs resolve to, and a typo shows up much later as a failing deploy. Listing
// the apps each credential covers turns that into something readable now.
type GitCredentialView struct {
	*db.GitCredential
	Apps []AppRef
	// SampleURL is a repository this credential covers, used to prefill the
	// connection test so the common case needs no typing.
	SampleURL string
}

// AppRef names an application without dragging its whole record into the page.
type AppRef struct {
	ID   string
	Name string
	Host string
}

func (s *Server) gitData(r *http.Request) map[string]any {
	creds, _ := db.ListGitCredentials(s.db)
	apps, _ := db.ListApps(s.db, s.keyring)

	// Every git app's forge host, so the page can show which credential covers
	// it — and, just as usefully, which hosts nothing covers yet.
	type gitApp struct {
		ref  AppRef
		url  string
		host string
	}
	var gitApps []gitApp
	for _, a := range apps {
		if a.DeployType != "git" || a.GitURL == "" {
			continue
		}
		host := db.GitHostOf(a.GitURL)
		gitApps = append(gitApps, gitApp{
			ref:  AppRef{ID: a.ID, Name: a.Name, Host: host},
			url:  a.GitURL,
			host: host,
		})
	}

	views := make([]GitCredentialView, 0, len(creds))
	covered := map[string]bool{}
	for _, c := range creds {
		v := GitCredentialView{GitCredential: c}
		for _, ga := range gitApps {
			// Resolve the same way a deploy does rather than comparing hosts by
			// hand, so the page cannot claim a match the deploy would not make.
			if ga.host == "" || !s.credentialWins(c, ga.host) {
				continue
			}
			v.Apps = append(v.Apps, ga.ref)
			covered[ga.host] = true
			if v.SampleURL == "" {
				v.SampleURL = ga.url
			}
		}
		views = append(views, v)
	}

	// Hosts an app clones from that no credential answers for. Public
	// repositories live here quite legitimately, which is why this is worded
	// as an observation rather than a problem.
	var uncovered []AppRef
	for _, ga := range gitApps {
		if ga.host != "" && !covered[ga.host] {
			uncovered = append(uncovered, ga.ref)
		}
	}
	sort.Slice(uncovered, func(i, j int) bool { return uncovered[i].Host < uncovered[j].Host })

	return map[string]any{
		"Title":       "Git credentials",
		"Nav":         "settings",
		"Credentials": views,
		"Uncovered":   uncovered,
		"Providers":   gitProviders,
		"AnyHost":     db.AnyHost,
		"DefaultUser": db.DefaultGitUsername,
	}
}

// credentialWins reports whether c is the credential a clone from host would
// actually resolve to — not merely one that could match it. A host with its
// own row is never served by the fallback, so the fallback must not claim it.
func (s *Server) credentialWins(c *db.GitCredential, host string) bool {
	winner := db.GitCredentialFor(s.db, s.keyring, host)
	return winner != nil && winner.ID == c.ID
}

func (s *Server) handleGitCredentials(w http.ResponseWriter, r *http.Request) {
	data := s.gitData(r)
	if msg := r.URL.Query().Get("msg"); msg != "" {
		data["Saved"] = msg
	}
	if e := r.URL.Query().Get("err"); e != "" {
		data["Error"] = e
	}
	s.render(w, r, "git_credentials", data)
}

func redirectGit(w http.ResponseWriter, r *http.Request, key, msg string) {
	http.Redirect(w, r, "/settings/git?"+key+"="+url.QueryEscape(msg), http.StatusSeeOther)
}

// handleGitCredentialSave adds a credential or updates the one already held
// for the same host.
func (s *Server) handleGitCredentialSave(w http.ResponseWriter, r *http.Request) {
	host := db.NormalizeGitHost(r.FormValue("host"))
	if host == "" {
		redirectGit(w, r, "err", "A host is required — the domain repositories are cloned from, or * for any.")
		return
	}
	cred := &db.GitCredential{
		Name:     strings.TrimSpace(r.FormValue("name")),
		Host:     host,
		Username: strings.TrimSpace(r.FormValue("username")),
		Secret:   strings.TrimSpace(r.FormValue("secret")),
	}
	if err := db.SaveGitCredential(s.db, s.keyring, cred); err != nil {
		redirectGit(w, r, "err", "Nothing is stored for "+host+" yet, so a token is required.")
		return
	}
	// The token itself never reaches the audit log; which host gained one does.
	s.audit(r, "git-credential.save", host, cred.Name)
	redirectGit(w, r, "msg", "Credential saved for "+hostLabel(host)+".")
}

func (s *Server) handleGitCredentialDelete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	db.DeleteGitCredential(s.db, id)
	s.audit(r, "git-credential.delete", r.PathValue("id"), "")
	redirectGit(w, r, "msg", "Credential deleted. Applications cloning from that host fall back to any-host credentials, or to anonymous access.")
}

// handleGitCredentialTest clones nothing but authenticates exactly as a deploy
// would, which is the only check that proves a token before a deploy needs it.
func (s *Server) handleGitCredentialTest(w http.ResponseWriter, r *http.Request) {
	repo := strings.TrimSpace(r.FormValue("repo_url"))
	if repo == "" {
		redirectGit(w, r, "err", "Enter a repository URL to test against — any private repository this credential should be able to reach.")
		return
	}
	if !strings.HasPrefix(repo, "https://") {
		redirectGit(w, r, "err", "Test with an https:// URL: that is the only kind Quasar attaches a credential to.")
		return
	}
	msg, err := s.dock.CheckGitAccess(r.Context(), repo)
	if err != nil {
		redirectGit(w, r, "err", "Could not reach "+repo+" — "+err.Error())
		return
	}
	redirectGit(w, r, "msg", msg)
}

// hostLabel names a host the way the page talks about it, so the any-host
// wildcard does not surface as a bare asterisk in a sentence.
func hostLabel(host string) string {
	if host == db.AnyHost {
		return "any host"
	}
	return host
}
