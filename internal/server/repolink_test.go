package server

import (
	"bytes"
	"strings"
	"testing"

	"quasar/internal/db"
	"quasar/internal/docker"
)

func TestRepoLinkOf(t *testing.T) {
	cases := []struct {
		raw  string
		want RepoLink
	}{
		{"https://github.com/acme/api.git",
			RepoLink{"https://github.com/acme/api", "acme/api", forgeGitHub}},
		{"https://github.com/acme/api",
			RepoLink{"https://github.com/acme/api", "acme/api", forgeGitHub}},
		{"https://github.com/acme/api/",
			RepoLink{"https://github.com/acme/api", "acme/api", forgeGitHub}},
		{"git@github.com:acme/api.git",
			RepoLink{"https://github.com/acme/api", "acme/api", forgeGitHub}},
		{"ssh://git@gitlab.com:2222/group/sub/proj.git",
			RepoLink{"https://gitlab.com/group/sub/proj", "group/sub/proj", forgeGitLab}},
		{"https://gitlab.example.com/team/app.git",
			RepoLink{"https://gitlab.example.com/team/app", "team/app", forgeGitLab}},
		{"https://bitbucket.org/acme/site.git",
			RepoLink{"https://bitbucket.org/acme/site", "acme/site", forgeBitbucket}},
		{"https://codeberg.org/me/tool.git",
			RepoLink{"https://codeberg.org/me/tool", "me/tool", forgeGitea}},
		{"https://git.example.com:3000/me/tool.git",
			RepoLink{"https://git.example.com:3000/me/tool", "me/tool", ""}},
		{"http://gitea.lan/me/tool",
			RepoLink{"http://gitea.lan/me/tool", "me/tool", forgeGitea}},
	}
	for _, c := range cases {
		got, ok := repoLinkOf(c.raw)
		if !ok || got != c.want {
			t.Errorf("repoLinkOf(%q) = %+v, %v; want %+v", c.raw, got, ok, c.want)
		}
	}
}

// A token pasted into the clone URL is a secret; the link is on the page.
func TestRepoLinkOfDropsCredentials(t *testing.T) {
	got, ok := repoLinkOf("https://oauth2:glpat-secret@gitlab.com/team/app.git")
	if !ok || got.URL != "https://gitlab.com/team/app" {
		t.Errorf("repoLinkOf kept or lost too much: %+v, %v", got, ok)
	}
}

func TestRepoLinkOfNoPage(t *testing.T) {
	for _, raw := range []string{
		"", "/srv/repos/app.git", "./app", `C:\repos\app`, "C:/repos/app",
		"file:///srv/repos/app.git", "https://github.com/", "git@github.com:",
	} {
		if got, ok := repoLinkOf(raw); ok {
			t.Errorf("repoLinkOf(%q) = %+v, want no link", raw, got)
		}
	}
}

// The page links the repository under the app's own address, drawn as its
// forge, and never repeats a token the clone URL was saved with.
func TestAppDetailLinksRepository(t *testing.T) {
	s := testServer(t)
	v := AppView{
		App: &db.App{
			ID: "beef0001", Name: "API", Subdomain: "api", DeployType: "git",
			GitURL: "https://oauth2:glpat-secret@gitlab.com/team/api.git", GitBranch: "main",
		},
		Status: docker.AppStatus{State: "running"},
		Domain: "example.com",
	}
	var buf bytes.Buffer
	if err := s.pages["app_detail"].ExecuteTemplate(&buf, "layout", map[string]any{"Title": "API", "App": v}); err != nil {
		t.Fatal(err)
	}
	html := buf.String()
	if !strings.Contains(html, `href="https://gitlab.com/team/api"`) {
		t.Error("page does not link the repository")
	}
	if !strings.Contains(html, "· main") {
		t.Error("page does not name the branch")
	}
	if strings.Contains(html, "glpat-secret") {
		t.Error("page shows the token saved in the clone URL")
	}
}

// The list reaches the repository from the type column, and only for an app
// that has one.
func TestAppsTableLinksRepository(t *testing.T) {
	s := testServer(t)
	rows := []AppView{{
		App:    &db.App{ID: "beef0001", Name: "API", Subdomain: "api", DeployType: "git", GitURL: "git@github.com:acme/api.git"},
		Domain: "example.com",
	}, {
		App:    &db.App{ID: "abcd1234", Name: "Blog", Subdomain: "blog", DeployType: "image", ImageRef: "nginx"},
		Domain: "example.com",
	}}
	var buf bytes.Buffer
	if err := s.pages["dashboard"].ExecuteTemplate(&buf, "apps_table", rows); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(buf.String(), `aria-label="Repository `); n != 1 {
		t.Errorf("list links %d repositories, want 1", n)
	}
	if !strings.Contains(buf.String(), `href="https://github.com/acme/api"`) {
		t.Error("list does not link the git app's repository")
	}
}
