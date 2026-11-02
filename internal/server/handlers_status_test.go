package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"quasar/internal/db"
)

func postStatus(s *Server, form url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/settings/status", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.handleStatusSettingsSave(rec, req)
	return rec
}

// Saving replaces the published applications and every section with what the
// form says: a box left unticked is off.
func TestSavingTheStatusSettingsReplacesThem(t *testing.T) {
	s := statusTestServer(t)
	rec := postStatus(s, url.Values{
		"mode": {"public"}, "cors": {"on"}, "show_uptime": {"on"},
		"title": {"  Example  "}, "link": {"https://example.com"},
		"publish": {"secret", "unknown-app"}, "name_secret": {" Tools "}, "name_cloud": {"ignored"},
	})
	if rec.Code != http.StatusSeeOther && rec.Code != http.StatusFound {
		t.Fatalf("save answered %d", rec.Code)
	}
	cfg := loadStatusConfig(s.db)
	if cfg.Mode != "public" || !cfg.CORS || cfg.ShowState || !cfg.ShowUptime || cfg.ShowIncidents || cfg.ShowServer ||
		cfg.Title != "Example" || cfg.Link != "https://example.com" {
		t.Errorf("saved = %+v", cfg)
	}
	published, _ := db.StatusApps(s.db)
	if len(published) != 1 || published["secret"] != "Tools" {
		t.Errorf("published = %v, want only the known, ticked application under its trimmed name", published)
	}
}

// What would be published wrong is not saved at all.
func TestTheStatusSettingsRefuseWhatTheyCannotPublish(t *testing.T) {
	s := statusTestServer(t)
	for name, form := range map[string]url.Values{
		"an unknown mode": {"mode": {"everyone"}},
		"a script link":   {"mode": {"public"}, "link": {"javascript:alert(1)"}},
		"a long title":    {"mode": {"public"}, "title": {strings.Repeat("x", statusTitleMax+1)}},
		"a long app name": {"mode": {"public"}, "publish": {"cloud"}, "name_cloud": {strings.Repeat("x", statusNameMax+1)}},
	} {
		postStatus(s, form)
		if got := loadStatusConfig(s.db).Mode; got != "off" {
			t.Errorf("%s was saved: mode is %s", name, got)
		}
	}
}

// The page may not take the dashboard's subdomain or an application's, and an
// application may not take the page's once it is deployed.
func TestTheStatusPageAndTheApplicationsKeepToTheirSubdomains(t *testing.T) {
	s := statusTestServer(t)
	for sub, ok := range map[string]bool{"status": true, "admin": false, "cloud": false, "Bad_Name": false, "": false} {
		if got := s.statusPageProblem(sub) == ""; got != ok {
			t.Errorf("page on %q allowed = %v, want %v", sub, got, ok)
		}
	}
	app := &db.App{Name: "x", Subdomain: "status", DeployType: "image", ImageRef: "nginx"}
	if problem := s.validateNewApp(app); problem != "" {
		t.Fatalf("with no page deployed, status is free: %s", problem)
	}
	setStatus(t, s, map[string]string{db.SettingStatusPage: "true", db.SettingStatusSubdomain: "status"})
	if problem := s.validateNewApp(app); !strings.Contains(problem, "status page") {
		t.Errorf("an application took the deployed page's subdomain: %q", problem)
	}
}
