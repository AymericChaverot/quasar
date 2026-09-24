package server

import (
	"bytes"
	"strings"
	"testing"

	"quasar/internal/db"
	"quasar/internal/docker"
)

func TestGitSourceProblem(t *testing.T) {
	a := &db.App{GitURL: "https://github.com/me/portfolio.git", GitBranch: "main"}
	cases := []struct {
		url, branch string
		refused     bool
	}{
		{"https://github.com/me/portfolio-v2.git", "main", false},
		{"https://github.com/me/portfolio.git", "v2", false},
		{"https://github.com/me/portfolio.git", "feature/new-home", false},
		{"https://github.com/me/portfolio.git", "main", true},
		{"", "main", true},
		{"--upload-pack=touch /tmp/x", "main", true},
		{"https://github.com/me/portfolio.git", "--orphan", true},
		{"https://github.com/me/portfolio.git", "two words", true},
	}
	for _, c := range cases {
		if got := gitSourceProblem(a, c.url, c.branch); (got != "") != c.refused {
			t.Errorf("gitSourceProblem(%q, %q) = %q, refused want %v", c.url, c.branch, got, c.refused)
		}
	}
}

// The form is the one place the stored clone URL reaches the page, and it
// must not bring a pasted token with it.
func TestGitSourcePanelHidesTheToken(t *testing.T) {
	s := testServer(t)
	v := AppView{
		App: &db.App{
			ID: "beef0001", Name: "API", Subdomain: "api", DeployType: "git",
			GitURL: "https://oauth2:glpat-secret@gitlab.com/team/api.git", GitBranch: "main",
		},
		Status:  docker.AppStatus{State: "running"},
		Domain:  "example.com",
		IsAdmin: true,
	}
	var buf bytes.Buffer
	data := map[string]any{"Title": "API", "App": v, "IsAdmin": true}
	if err := s.pages["app_detail"].ExecuteTemplate(&buf, "layout", data); err != nil {
		t.Fatal(err)
	}
	html := buf.String()
	if !strings.Contains(html, `hx-post="/apps/beef0001/source"`) {
		t.Fatal("the page has no form to change the source")
	}
	if strings.Contains(html, "glpat-secret") {
		t.Error("the source form shows the token saved in the clone URL")
	}
	if !strings.Contains(html, `value="https://***@gitlab.com/team/api.git"`) {
		t.Error("the source form does not show the masked URL")
	}
}

// The form sits among the admin's controls; a viewer only gets the link.
func TestGitSourcePanelIsForAdmins(t *testing.T) {
	s := testServer(t)
	v := AppView{
		App:    &db.App{ID: "beef0001", Name: "API", Subdomain: "api", DeployType: "git", GitURL: "https://github.com/me/api.git", GitBranch: "main"},
		Domain: "example.com",
	}
	var buf bytes.Buffer
	if err := s.pages["app_detail"].ExecuteTemplate(&buf, "layout", map[string]any{"Title": "API", "App": v}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "/source\"") {
		t.Error("a viewer is offered the form to change the source")
	}
}
