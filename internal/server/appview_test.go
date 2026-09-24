package server

import (
	"testing"

	"quasar/internal/db"
)

func TestAppViewHost(t *testing.T) {
	cases := []struct {
		subdomain string
		want      string
	}{
		{"blog", "blog.example.com"},
		{"@", "example.com"},
	}
	for _, c := range cases {
		v := AppView{App: &db.App{Subdomain: c.subdomain}, Domain: "example.com"}
		if got := v.Host(); got != c.want {
			t.Errorf("Host() with subdomain %q = %q, want %q", c.subdomain, got, c.want)
		}
	}
}

func TestAppViewRepo(t *testing.T) {
	git := AppView{App: &db.App{DeployType: "git", GitURL: "git@github.com:acme/api.git"}}
	if r := git.Repo(); r == nil || r.URL != "https://github.com/acme/api" {
		t.Errorf("Repo() of a git app = %+v", r)
	}
	image := AppView{App: &db.App{DeployType: "image", GitURL: "https://github.com/acme/api"}}
	if r := image.Repo(); r != nil {
		t.Errorf("Repo() of an image app = %+v, want nil", r)
	}
	local := AppView{App: &db.App{DeployType: "git", GitURL: "/srv/repos/api.git"}}
	if r := local.Repo(); r != nil {
		t.Errorf("Repo() of a local checkout = %+v, want nil", r)
	}
}
