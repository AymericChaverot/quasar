package server

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"quasar/internal/auth"
	"quasar/internal/db"
)

func apiTestServer(t *testing.T) (*Server, *sql.DB) {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	return &Server{db: database, guards: map[string]string{}, mux: http.NewServeMux()}, database
}

func TestRequireToken(t *testing.T) {
	s, database := apiTestServer(t)
	adminToken, err := auth.CreateToken(database, "deployer", auth.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	viewerToken, err := auth.CreateToken(database, "monitoring", auth.RoleViewer)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		header     string
		adminOnly  bool
		wantStatus int
	}{
		{name: "no header", header: "", wantStatus: http.StatusUnauthorized},
		{name: "not a bearer token", header: "Basic dXNlcjpwYXNz", wantStatus: http.StatusUnauthorized},
		{name: "unknown token", header: "Bearer qsr_deadbeef", wantStatus: http.StatusUnauthorized},
		{name: "viewer on a read route", header: "Bearer " + viewerToken, wantStatus: http.StatusOK},
		{name: "admin on a read route", header: "Bearer " + adminToken, wantStatus: http.StatusOK},
		// The distinction that matters: a real credential, but not one allowed
		// to change anything.
		{name: "viewer on a write route", header: "Bearer " + viewerToken, adminOnly: true, wantStatus: http.StatusForbidden},
		{name: "admin on a write route", header: "Bearer " + adminToken, adminOnly: true, wantStatus: http.StatusOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reached := false
			h := s.requireToken(tc.adminOnly, func(w http.ResponseWriter, r *http.Request) {
				reached = true
				w.WriteHeader(http.StatusOK)
			})
			req := httptest.NewRequest("POST", "/api/v1/apps/x/deploy", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d (body %q)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if reached != (tc.wantStatus == http.StatusOK) {
				t.Errorf("handler reached = %v at status %d", reached, rec.Code)
			}
			if rec.Code != http.StatusOK && !strings.Contains(rec.Body.String(), `"error"`) {
				t.Errorf("a rejection should return a JSON error, got %q", rec.Body.String())
			}
		})
	}
}

// A session cookie must not open the API: the dashboard and the API share an
// origin, so a cookie-authenticated JSON endpoint would be reachable from any
// page the operator visits while signed in.
func TestSessionCookieDoesNotAuthenticateTheAPI(t *testing.T) {
	s, database := apiTestServer(t)
	if err := auth.EnsureAdmin(database, "root", "password123"); err != nil {
		t.Fatal(err)
	}
	token, _, err := auth.Login(database, "root", "password123")
	if err != nil {
		t.Fatal(err)
	}

	h := s.requireToken(false, func(w http.ResponseWriter, r *http.Request) {
		t.Error("a session cookie reached an API handler")
	})
	req := httptest.NewRequest("GET", "/api/v1/apps", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: token})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

// The token's name has to reach the audit trail, or an API deploy is
// unattributable.
func TestTokenNameReachesTheHandler(t *testing.T) {
	s, database := apiTestServer(t)
	token, err := auth.CreateToken(database, "github-actions", auth.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}

	var seen string
	h := s.requireToken(true, func(w http.ResponseWriter, r *http.Request) {
		seen, _ = r.Context().Value(apiTokenKey{}).(string)
	})
	req := httptest.NewRequest("POST", "/api/v1/apps/x/deploy", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	h.ServeHTTP(httptest.NewRecorder(), req)

	if seen != "github-actions" {
		t.Errorf("token name in context = %q, want github-actions", seen)
	}
}

// An API token lives in CI configuration, which is a weaker place than the
// encrypted column an app's secrets sit in. Embedding *db.App here — the
// obvious shortcut when adding a field — would leak all of them at once, so the
// response shape is pinned.
func TestAPIAppExposesNoSecrets(t *testing.T) {
	allowed := map[string]bool{
		"ID": true, "Name": true, "Subdomain": true, "Host": true,
		"DeployType": true, "ImageRef": true, "GitURL": true, "GitBranch": true,
		"Port": true, "State": true, "Uptime": true,
	}
	forbidden := map[string]bool{
		"EnvContent": true, "ComposeYAML": true, "PreBackupCmd": true,
		"WebhookSecret": true, "BasicAuthHash": true, "BasicAuthUser": true,
		"App": true, // an embedded *db.App would carry every one of the above
	}

	typ := reflect.TypeOf(apiApp{})
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		if forbidden[name] {
			t.Errorf("apiApp exposes %q, which carries application secrets", name)
		}
		if !allowed[name] {
			t.Errorf("apiApp has a new field %q — confirm it holds nothing secret, then add it here", name)
		}
	}
}
