package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"quasar/internal/auth"
	"quasar/internal/config"
	"quasar/internal/db"
	"quasar/internal/docker"
	"quasar/internal/secrets"
)

// envTestServer is an API server with one application, a Docker client that
// only ever writes files, and the three kinds of token that matter here.
func envTestServer(t *testing.T) (s *Server, appsDir string, tokens map[string]string) {
	t.Helper()
	s, database := apiTestServer(t)
	appsDir = t.TempDir()
	keyring, err := secrets.LoadOrCreateKey(filepath.Join(t.TempDir(), "master.key"))
	if err != nil {
		t.Fatal(err)
	}
	s.keyring = keyring
	dock, err := docker.New(config.Config{AppsDir: appsDir}, database, keyring)
	if err != nil {
		t.Fatal(err)
	}
	s.dock = dock
	s.routes()
	app := &db.App{ID: "a1", Name: "blog", Subdomain: "blog", DeployType: "image", ImageRef: "nginx",
		EnvContent: "# the database\nDATABASE_URL=postgres://u:p@db/blog\nDEBUG=0"}
	if err := db.InsertApp(database, s.keyring, app); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(appsDir, "a1"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, u := range []struct{ name, role string }{{"alice", auth.RoleAdmin}, {"bob", auth.RoleViewer}} {
		if err := auth.CreateUser(database, u.name, "password123", u.role); err != nil {
			t.Fatal(err)
		}
	}
	users, _ := auth.ListUsers(database)
	tokens = map[string]string{}
	for _, u := range users {
		tokens[u.Username], _ = auth.CreatePersonalToken(database, u.ID, "laptop", 0)
	}
	tokens["service"], _ = auth.CreateToken(database, "ci", auth.RoleAdmin)
	return s, appsDir, tokens
}

func envCall(s *Server, method, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/api/v1/apps/a1/env", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	return rec
}

// Only an admin's personal token reads the values, and the read is audited
// without them.
func TestAPIEnvRead(t *testing.T) {
	s, _, tokens := envTestServer(t)

	if rec := envCall(s, "GET", tokens["service"], ""); rec.Code != http.StatusForbidden {
		t.Errorf("service token: status %d, want 403", rec.Code)
	}
	if rec := envCall(s, "GET", tokens["bob"], ""); rec.Code != http.StatusForbidden {
		t.Errorf("a viewer's personal token: status %d, want 403", rec.Code)
	}
	rec := envCall(s, "GET", tokens["alice"], "")
	if rec.Code != http.StatusOK {
		t.Fatalf("an admin's personal token: status %d (%s)", rec.Code, rec.Body.String())
	}
	var got apiEnv
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Content != "# the database\nDATABASE_URL=postgres://u:p@db/blog\nDEBUG=0" {
		t.Errorf("content = %q, want the text as stored, comments included", got.Content)
	}

	entries, _ := db.ListAudit(s.db, "", 10)
	found := false
	for _, e := range entries {
		if e.Action == "app.env-read" {
			found = true
			if e.Actor != "alice via laptop" || strings.Contains(e.Detail, "postgres") {
				t.Errorf("audit entry %+v: want alice's token, and no values", e)
			}
		}
	}
	if !found {
		t.Error("reading the environment left no audit entry")
	}
}

// Writing replaces the text, stores it, and writes the file the next deploy
// reads; an admin token of either kind may do it.
func TestAPIEnvWrite(t *testing.T) {
	s, appsDir, tokens := envTestServer(t)

	if rec := envCall(s, "PUT", tokens["bob"], `{"content":"X=1"}`); rec.Code != http.StatusForbidden {
		t.Errorf("a viewer's token: status %d, want 403", rec.Code)
	}
	for _, bad := range []string{``, `{}`, `{"content": 3}`, `{"content":"X=1","extra":true}`, `not json`} {
		if rec := envCall(s, "PUT", tokens["alice"], bad); rec.Code != http.StatusBadRequest {
			t.Errorf("body %q: status %d, want 400", bad, rec.Code)
		}
	}

	rec := envCall(s, "PUT", tokens["service"], `{"content":"# kept\r\nDEBUG=1\r\n"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d (%s)", rec.Code, rec.Body.String())
	}
	a, err := db.GetApp(s.db, s.keyring, "a1")
	if err != nil {
		t.Fatal(err)
	}
	if a.EnvContent != "# kept\nDEBUG=1\n" {
		t.Errorf("stored %q, want the text with plain newlines", a.EnvContent)
	}
	file, err := os.ReadFile(filepath.Join(appsDir, "a1", ".env"))
	if err != nil || !strings.Contains(string(file), "DEBUG=1") {
		t.Errorf("the .env beside the app reads %q (err %v)", file, err)
	}
}
