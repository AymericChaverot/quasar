package server

import (
	"testing"

	"quasar/internal/certs"
	"quasar/internal/config"
	"quasar/internal/db"
)

// A certificate may only be deleted when nothing routes it any more, so the
// map of live hostnames is what the whole guard rests on.
func TestRoutedHosts(t *testing.T) {
	s := &Server{cfg: config.Config{Domain: "example.com"}}
	routed := s.routedHosts([]*db.App{
		{Name: "Blog", Subdomain: "blog", CustomDomains: "www.myblog.fr, myblog.fr"},
		{Name: "Site", Subdomain: "@"}, // claims the apex
	})

	want := map[string]string{
		"admin.example.com": "this dashboard",
		"blog.example.com":  "Blog",
		"www.myblog.fr":     "Blog",
		"myblog.fr":         "Blog",
		"example.com":       "Site",
	}
	for host, who := range want {
		if got := routed[host]; got != who {
			t.Errorf("routedHosts()[%q] = %q, want %q", host, got, who)
		}
	}
	if len(routed) != len(want) {
		t.Errorf("routedHosts() has %d entries, want %d: %v", len(routed), len(want), routed)
	}
}

func TestMatchCerts(t *testing.T) {
	routed := map[string]string{
		"admin.example.com": "this dashboard",
		"blog.example.com":  "Blog",
		"www.myblog.fr":     "Blog",
	}
	got := matchCerts([]certs.Cert{
		{Domain: "admin.example.com"},
		// Claimed through a SAN rather than its main domain: still in use.
		{Domain: "myblog.fr", SANs: []string{"myblog.fr", "www.myblog.fr"}},
		{Domain: "gone.example.com", SANs: []string{"gone.example.com"}},
		{Domain: "BLOG.example.com"},
	}, routed)

	wantUsedBy := []string{"this dashboard", "Blog", "", "Blog"}
	for i, want := range wantUsedBy {
		if got[i].UsedBy != want {
			t.Errorf("cert %q UsedBy = %q, want %q", got[i].Domain, got[i].UsedBy, want)
		}
	}
}
