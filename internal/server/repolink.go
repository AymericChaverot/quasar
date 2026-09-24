package server

import (
	"net/url"
	"strings"
)

// The forges a repository link can be drawn as. Anything else is still linked,
// under a plain git mark.
const (
	forgeGitHub    = "github"
	forgeGitLab    = "gitlab"
	forgeBitbucket = "bitbucket"
	forgeGitea     = "gitea"
)

// RepoLink is where a clone URL can be read in a browser.
type RepoLink struct {
	// URL is the repository's page, with any credential the clone URL carried
	// left behind.
	URL string
	// Label is the repository's path on its forge: "owner/repo", or longer on
	// a forge with nested groups.
	Label string
	// Forge picks the mark drawn next to the link; empty for one not known.
	Forge string
}

// repoLinkOf works out the page behind a clone URL, or reports false when
// there is none — a local path, or something that does not parse.
//
// An https URL is read as is. An ssh remote, either as ssh://git@host/path or
// in git's short git@host:path form, is served over https on the same host,
// which holds for every forge in use; its port is the ssh daemon's, not the web
// server's, so it is dropped.
//
// Whatever userinfo the clone URL carried stays out of the link: an operator
// may have pasted a token into it, and this is shown on the page.
func repoLinkOf(raw string) (RepoLink, bool) {
	raw = strings.TrimSpace(raw)
	scheme, host, path := "https", "", ""
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil {
			return RepoLink{}, false
		}
		switch u.Scheme {
		case "http", "https":
			scheme, host = u.Scheme, u.Host
		case "ssh", "git", "git+ssh", "ssh+git":
			host = u.Hostname()
		default:
			return RepoLink{}, false
		}
		path = u.Path
	} else {
		// The scp-like form: a colon before any slash, and more than one
		// letter ahead of it so a Windows drive is not taken for a host.
		before, after, ok := strings.Cut(raw, ":")
		if !ok || strings.Contains(before, "/") {
			return RepoLink{}, false
		}
		if i := strings.LastIndex(before, "@"); i >= 0 {
			before = before[i+1:]
		}
		if len(before) < 2 {
			return RepoLink{}, false
		}
		host, path = before, after
	}
	path = strings.Trim(path, "/")
	path = strings.TrimSuffix(path, ".git")
	path = strings.Trim(path, "/")
	if host == "" || path == "" {
		return RepoLink{}, false
	}
	return RepoLink{
		URL:   scheme + "://" + host + "/" + path,
		Label: path,
		Forge: forgeOf(host),
	}, true
}

// forgeOf names the forge a host runs. The public ones are known by name; a
// self-hosted GitLab or Gitea is only recognised when its host says so, and
// is otherwise linked under the plain mark rather than guessed at.
func forgeOf(host string) string {
	host = strings.ToLower(host)
	if h, _, ok := strings.Cut(host, ":"); ok {
		host = h
	}
	switch {
	case host == "github.com" || strings.HasSuffix(host, ".github.com"):
		return forgeGitHub
	case host == "bitbucket.org":
		return forgeBitbucket
	case host == "codeberg.org":
		return forgeGitea
	case strings.Contains(host, "gitlab"):
		return forgeGitLab
	case strings.Contains(host, "gitea"), strings.Contains(host, "forgejo"):
		return forgeGitea
	}
	return ""
}
