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
