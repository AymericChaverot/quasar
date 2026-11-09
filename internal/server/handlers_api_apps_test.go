package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"quasar/internal/db"
)

func apiCall(s *Server, method, path, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	return rec
}

// An application deployed from a local folder is created without a deploy —
// there is nothing to deploy until a folder is uploaded — and answered in the
// shape every other route uses.
func TestAPIAppCreate(t *testing.T) {
	s, _, tokens := envTestServer(t)

	rec := apiCall(s, "POST", "/api/v1/apps", tokens["alice"],
		`{"name": "Site", "subdomain": "Site", "deploy_type": "local", "port": 8080, "env_content": "A=1\r\nB=2"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var got apiApp
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.ID == "" || got.Subdomain != "site" || got.DeployType != "local" || got.Port != 8080 {
		t.Errorf("answered %+v", got)
	}
	stored, err := db.GetApp(s.db, s.keyring, got.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.EnvContent != "A=1\nB=2" {
		t.Errorf("env stored as %q", stored.EnvContent)
	}
	if d := s.dock.Deploying(got.ID); d != nil {
		t.Error("a local application must not be deployed before its first upload")
	}
	if !auditHas(t, s, "app.create", "alice via laptop") {
		t.Error("the creation should be audited against the token")
	}
}

func TestAPIAppCreateRefusals(t *testing.T) {
	s, _, tokens := envTestServer(t)
	for _, tc := range []struct {
		name, token, body string
		want              int
	}{
		{"viewer", tokens["bob"], `{"name": "x", "subdomain": "x", "deploy_type": "local"}`, http.StatusForbidden},
		{"not json", tokens["alice"], `name=x`, http.StatusBadRequest},
		{"unknown field", tokens["alice"], `{"name": "x", "subdomain": "x", "deploy_type": "local", "secret": 1}`, http.StatusBadRequest},
		{"no name", tokens["alice"], `{"subdomain": "x", "deploy_type": "local"}`, http.StatusBadRequest},
		{"bad type", tokens["alice"], `{"name": "x", "subdomain": "x", "deploy_type": "ftp"}`, http.StatusBadRequest},
		{"image without a ref", tokens["alice"], `{"name": "x", "subdomain": "x", "deploy_type": "image"}`, http.StatusBadRequest},
		{"bad port", tokens["alice"], `{"name": "x", "subdomain": "x", "deploy_type": "local", "port": 70000}`, http.StatusBadRequest},
		{"subdomain taken", tokens["alice"], `{"name": "x", "subdomain": "blog", "deploy_type": "local"}`, http.StatusConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := apiCall(s, "POST", "/api/v1/apps", tc.token, tc.body)
			if rec.Code != tc.want {
				t.Errorf("status %d, want %d: %s", rec.Code, tc.want, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), `"error"`) {
				t.Errorf("want a JSON error, got %s", rec.Body.String())
			}
		})
	}
}

// Deleting removes the application and its files, and is an admin's act.
func TestAPIAppDelete(t *testing.T) {
	s, appsDir, tokens := envTestServer(t)

	if rec := apiCall(s, "DELETE", "/api/v1/apps/a1", tokens["bob"], ""); rec.Code != http.StatusForbidden {
		t.Errorf("viewer: status %d", rec.Code)
	}
	if rec := apiCall(s, "DELETE", "/api/v1/apps/nope", tokens["alice"], ""); rec.Code != http.StatusNotFound {
		t.Errorf("unknown app: status %d", rec.Code)
	}
	rec := apiCall(s, "DELETE", "/api/v1/apps/a1", tokens["service"], "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := db.GetApp(s.db, s.keyring, "a1"); err == nil {
		t.Error("the application is still stored")
	}
	if _, err := os.Stat(filepath.Join(appsDir, "a1")); !os.IsNotExist(err) {
		t.Error("the application's directory is still there")
	}
	if !auditHas(t, s, "app.delete", "token:ci") {
		t.Error("the delete should be audited against the token")
	}
}

// auditHas reports whether the audit trail holds an action by actor.
func auditHas(t *testing.T, s *Server, action, actor string) bool {
	t.Helper()
	entries, err := db.ListAudit(s.db, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Action == action && e.Actor == actor {
			return true
		}
	}
	return false
}

// Only a git application reports the branch it is deployed from: the others
// store "main" by default, which would read as if they had one.
func TestAPIAppBranchOnlyForGit(t *testing.T) {
	git := apiAppOf(AppView{App: &db.App{DeployType: "git", GitBranch: "prod"}})
	local := apiAppOf(AppView{App: &db.App{DeployType: "local", GitBranch: "main"}})
	if git.GitBranch != "prod" || local.GitBranch != "" {
		t.Errorf("git branch %q, local branch %q", git.GitBranch, local.GitBranch)
	}
}
