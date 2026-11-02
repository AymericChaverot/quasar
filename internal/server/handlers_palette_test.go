package server

import (
	"strings"
	"testing"
)

// A viewer is offered no admin page and no action; an admin is offered both.
// Every page offered is one the dashboard serves to that role.
func TestPaletteOffersWhatTheRoleReaches(t *testing.T) {
	s := newTestServer(t)

	for _, admin := range []bool{false, true} {
		pages, actions, themesOffered := paletteChoices(admin, false, "paper")
		hrefs := map[string]bool{}
		for _, p := range pages {
			hrefs[p.Href] = true
			path, _, _ := strings.Cut(p.Href, "#")
			guard, ok := s.guards["GET "+path]
			if path == "/" {
				guard, ok = s.guards["GET /{$}"]
			}
			if !ok {
				t.Errorf("%s is offered but not served", p.Href)
				continue
			}
			if !admin && guard == accessAdmin {
				t.Errorf("a viewer is offered %s, which needs an admin", p.Href)
			}
		}
		if admin != hrefs["/settings/status"] || admin != (len(actions) > 0) {
			t.Errorf("admin %v: status page %v, %d actions", admin, hrefs["/settings/status"], len(actions))
		}
		for _, a := range actions {
			if _, ok := s.guards["POST "+a.Action]; !ok {
				t.Errorf("the action %s is not served", a.Action)
			}
		}
		current := 0
		for _, th := range themesOffered {
			if th.Current {
				current++
			}
		}
		if len(themesOffered) != len(themes) || current != 1 {
			t.Errorf("themes %+v", themesOffered)
		}
	}
	if pages, _, _ := paletteChoices(false, true, ""); pages[len(pages)-1].Href != "/stations" {
		t.Error("the Stations page is not offered once there is a station")
	}
}
