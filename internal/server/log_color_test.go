package server

import (
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"quasar/internal/db"
	"quasar/internal/secrets"
)

// A colour from the picker is stored as the application's; anything that is
// not one is refused and leaves the stored colour alone.
func TestAppLogColorIsSaved(t *testing.T) {
	s, database := catalogTestServer(t)
	k, err := secrets.LoadOrCreateKey(filepath.Join(t.TempDir(), "master.key"))
	if err != nil {
		t.Fatal(err)
	}
	s.keyring = k
	if err := db.InsertApp(database, k, &db.App{ID: "a1", Name: "Web", Subdomain: "web", DeployType: "image", ImageRef: "nginx"}); err != nil {
		t.Fatal(err)
	}
	s.mux.HandleFunc("POST /apps/{id}/log-color", s.handleAppLogColor)
	send := func(color string) int {
		r := httptest.NewRequest("POST", "/apps/a1/log-color", strings.NewReader(url.Values{"log_color": {color}}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		s.mux.ServeHTTP(w, r)
		return w.Code
	}

	if code := send("#12AB34"); code != 200 {
		t.Fatalf("saving a colour answered %d", code)
	}
	if code := send("red; background: url(x)"); code != 400 {
		t.Errorf("something that is not a colour answered %d, want 400", code)
	}
	a, _ := db.GetApp(database, k, "a1")
	if a.LogColor != "#12ab34" {
		t.Errorf("stored colour %q, want #12ab34", a.LogColor)
	}
}
