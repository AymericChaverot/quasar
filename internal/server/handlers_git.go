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

	// Every git app, resolved the way a deploy resolves it. Doing it once here
	// is what lets the page show which credential each application actually
	// gets — and, just as usefully, which repositories nothing covers yet.
	type gitApp struct {
		ref    AppRef
		url    string
		winner *db.GitCredential
	}
	var gitApps []gitApp
	for _, a := range apps {
		if a.DeployType != "git" || a.GitURL == "" {
			continue
		}
		target := db.GitTargetOf(a.GitURL)
		if target == "" {
			continue // ssh or a local path: nothing on this page applies
		}
		gitApps = append(gitApps, gitApp{
			ref:    AppRef{ID: a.ID, Name: a.Name, Host: target},
			url:    a.GitURL,
			winner: db.GitCredentialFor(s.db, s.keyring, a.GitURL),
		})
	}

	views := make([]GitCredentialView, 0, len(creds))
	for _, c := range creds {
		v := GitCredentialView{GitCredential: c}
		for _, ga := range gitApps {
			// Only the credential that actually wins is listed, so a forge-wide
			// token never claims a repository an owner-scoped one has taken.
			if ga.winner == nil || ga.winner.ID != c.ID {
				continue
			}
			v.Apps = append(v.Apps, ga.ref)
			if v.SampleURL == "" {
				v.SampleURL = ga.url
			}
		}
		views = append(views, v)
	}

	// Repositories no credential answers for. Public ones live here quite
	// legitimately, which is why this is worded as an observation rather than
	// as a problem.
	var uncovered []AppRef
	for _, ga := range gitApps {
		if ga.winner == nil {
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
		"AnyScope":    db.AnyScope,
		"DefaultUser": db.DefaultGitUsername,
	}
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
// for the same scope.
func (s *Server) handleGitCredentialSave(w http.ResponseWriter, r *http.Request) {
	scope := db.NormalizeGitScope(r.FormValue("scope"))
	if scope == "" {
		redirectGit(w, r, "err", "A scope is required — a host, a host and owner, or * for everything.")
		return
	}
	cred := &db.GitCredential{
		Name:     strings.TrimSpace(r.FormValue("name")),
		Scope:    scope,
		Username: strings.TrimSpace(r.FormValue("username")),
		Secret:   strings.TrimSpace(r.FormValue("secret")),
	}
	if err := db.SaveGitCredential(s.db, s.keyring, cred); err != nil {
		redirectGit(w, r, "err", "Nothing is stored for "+scopeLabel(scope)+" yet, so a token is required.")
		return
	}
	// The token itself never reaches the audit log; which scope gained one does.
	s.audit(r, "git-credential.save", scope, cred.Name)
	redirectGit(w, r, "msg", "Credential saved for "+scopeLabel(scope)+".")
}

func (s *Server) handleGitCredentialDelete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	db.DeleteGitCredential(s.db, id)
	s.audit(r, "git-credential.delete", r.PathValue("id"), "")
	redirectGit(w, r, "msg", "Credential deleted. Repositories it covered fall back to the next widest credential, or to anonymous access.")
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

// scopeLabel names a scope the way the page talks about it, so the catch-all
// does not surface as a bare asterisk in the middle of a sentence.
func scopeLabel(scope string) string {
	if scope == db.AnyScope {
		return "every repository not covered by another credential"
	}
	return scope
}
