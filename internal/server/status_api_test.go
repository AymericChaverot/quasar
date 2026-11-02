package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"quasar/internal/auth"
	"quasar/internal/db"
	"quasar/internal/secrets"
	"quasar/internal/vps"
)

// statusTestServer is a server with three applications: two published, one
// of them under another name and without a health check, and one not.
func statusTestServer(t *testing.T) *Server {
	t.Helper()
	s, database := apiTestServer(t)
	keyring, err := secrets.LoadOrCreateKey(filepath.Join(t.TempDir(), "master.key"))
	if err != nil {
		t.Fatal(err)
	}
	s.keyring = keyring
	for _, a := range []*db.App{
		{ID: "cloud", Name: "nextcloud", Subdomain: "cloud", DeployType: "image", ImageRef: "nginx", HealthPath: "/status.php"},
		{ID: "photos", Name: "immich", Subdomain: "photos", DeployType: "image", ImageRef: "nginx"},
		{ID: "secret", Name: "internal-tool", Subdomain: "tool", DeployType: "image", ImageRef: "nginx", HealthPath: "/"},
	} {
		if err := db.InsertApp(database, keyring, a); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.SetStatusApps(database, []db.StatusApp{{AppID: "cloud", Name: ""}, {AppID: "photos", Name: "Photos"}}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"cloud", "secret"} {
		if _, err := database.Exec(`INSERT INTO health_history (app_id, ts, ok) VALUES (?, datetime('now', '-1 minutes'), 1)`, id); err != nil {
			t.Fatal(err)
		}
	}

	running, collect := statusRunning, vpsCollect
	statusRunning = func(_ *Server, _ context.Context, a *db.App) bool { return a.ID != "photos" }
	vpsCollect = func(string) (vps.Stats, error) {
		return vps.Stats{CPUPercent: 12.34, MemPercent: 50, DiskPercent: 70.06}, nil
	}
	t.Cleanup(func() { statusRunning, vpsCollect = running, collect })
	return s
}

func setStatus(t *testing.T, s *Server, kv map[string]string) {
	t.Helper()
	for k, v := range kv {
		if err := db.SetSetting(s.db, k, v); err != nil {
			t.Fatal(err)
		}
	}
	s.status.forget()
}

func getStatus(s *Server, header string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", "/api/v1/status", nil)
	if header != "" {
		req.Header.Set("Authorization", header)
	}
	rec := httptest.NewRecorder()
	s.handleStatusAPI(rec, req)
	return rec
}

// Off answers nobody but the page; a token mode wants any API token; public
// wants nothing.
func TestTheStatusAnswersWhomItsModeAllows(t *testing.T) {
	s := statusTestServer(t)
	token, err := auth.CreateToken(s.db, "widget", auth.RoleViewer)
	if err != nil {
		t.Fatal(err)
	}
	setStatus(t, s, map[string]string{db.SettingStatusPageKey: "page-key"})

	for _, c := range []struct {
		mode, header string
		want         int
	}{
		{"off", "", http.StatusNotFound},
		{"off", "Bearer " + token, http.StatusNotFound},
		{"off", "Bearer page-key", http.StatusOK},
		{"token", "", http.StatusUnauthorized},
		{"token", "Bearer qsr_nope", http.StatusUnauthorized},
		{"token", "Bearer " + token, http.StatusOK},
		{"token", "Bearer page-key", http.StatusOK},
		{"public", "", http.StatusOK},
		{"nonsense", "", http.StatusNotFound},
	} {
		setStatus(t, s, map[string]string{db.SettingStatusMode: c.mode})
		if got := getStatus(s, c.header).Code; got != c.want {
			t.Errorf("mode %s with %q: %d, want %d", c.mode, c.header, got, c.want)
		}
	}
}

// Only the published applications, under their public names; an application
// without a check has a state but no history; the incidents name only what is
// published.
func TestTheStatusPublishesOnlyWhatWasChosen(t *testing.T) {
	s := statusTestServer(t)
	for _, id := range []string{"cloud", "secret"} {
		if err := db.OpenIncident(s.db, id); err != nil {
			t.Fatal(err)
		}
	}
	setStatus(t, s, map[string]string{db.SettingStatusMode: "public", db.SettingStatusTitle: "Example"})

	rec := getStatus(s, "")
	body := rec.Body.String()
	if strings.Contains(body, "internal-tool") || strings.Contains(body, "immich") || strings.Contains(body, `"secret"`) {
		t.Fatalf("an unpublished application or an internal name leaked: %s", body)
	}
	var doc StatusDoc
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Title != "Example" || len(doc.Services) != 2 {
		t.Fatalf("doc = %+v", doc)
	}
	cloud, photos := doc.Services[0], doc.Services[1]
	if cloud.Name != "nextcloud" || cloud.Status != stateDown || cloud.Uptime == nil || len(cloud.Days) != uptimeDays {
		t.Errorf("cloud = %+v: want its own name, down for its open incident, and ninety days", cloud)
	}
	if cloud.Uptime != nil && (cloud.Uptime.Today == nil || *cloud.Uptime.Today != 100) {
		t.Errorf("cloud's uptime today = %v", cloud.Uptime.Today)
	}
	if photos.Name != "Photos" || photos.Status != stateDown || photos.Uptime != nil || photos.Days != nil {
		t.Errorf("photos = %+v: want down from its container, and no history without a check", photos)
	}
	if doc.Status != stateMajorOutage {
		t.Errorf("overall = %s with every service down", doc.Status)
	}
	if doc.Incidents == nil || len(*doc.Incidents) != 1 || (*doc.Incidents)[0].Service != "nextcloud" {
		t.Errorf("incidents = %+v, want only the published one", doc.Incidents)
	}
	if doc.Server != nil || strings.Contains(body, `"server"`) {
		t.Errorf("the server's health is off by default and was published: %s", body)
	}
}

// A section turned off is absent, not empty; the server's health is rounded.
func TestATurnedOffSectionIsAbsent(t *testing.T) {
	s := statusTestServer(t)
	setStatus(t, s, map[string]string{
		db.SettingStatusMode: "public", db.SettingStatusShowState: "false",
		db.SettingStatusShowUptime: "false", db.SettingStatusShowIncidents: "false",
		db.SettingStatusShowServer: "true",
	})
	body := getStatus(s, "").Body.String()
	for _, absent := range []string{`"status"`, `"uptime"`, `"days"`, `"incidents"`} {
		if strings.Contains(body, absent) {
			t.Errorf("%s is turned off and still in %s", absent, body)
		}
	}
	if !strings.Contains(body, `"server":{"cpu":12.3,"memory":50,"disk":70.1}`) {
		t.Errorf("server = %s", body)
	}

	setStatus(t, s, map[string]string{db.SettingStatusShowIncidents: "true"})
	if body := getStatus(s, "").Body.String(); !strings.Contains(body, `"incidents":[]`) {
		t.Errorf("no incident with the section on should read as an empty list: %s", body)
	}
}

// Other sites may read the status only when the operator said so.
func TestOtherSitesReadTheStatusOnlyWhenAllowed(t *testing.T) {
	s := statusTestServer(t)
	setStatus(t, s, map[string]string{db.SettingStatusMode: "public"})
	if got := getStatus(s, "").Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("CORS off and the header says %q", got)
	}
	setStatus(t, s, map[string]string{db.SettingStatusCORS: "true"})
	if got := getStatus(s, "").Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("CORS on and the header says %q", got)
	}
	rec := httptest.NewRecorder()
	s.handleStatusPreflight(rec, httptest.NewRequest("OPTIONS", "/api/v1/status", nil))
	if rec.Code != http.StatusNoContent || rec.Header().Get("Access-Control-Allow-Headers") != "Authorization" {
		t.Errorf("preflight = %d %v", rec.Code, rec.Header())
	}
}
