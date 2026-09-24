package server

import "testing"

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
