package server

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"quasar/internal/auth"
	"quasar/internal/db"
	"quasar/internal/docker"
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
		seen = apiActor(apiIdentity(r))
	})
	req := httptest.NewRequest("POST", "/api/v1/apps/x/deploy", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	h.ServeHTTP(httptest.NewRecorder(), req)

	if seen != "token:github-actions" {
		t.Errorf("actor = %q, want token:github-actions", seen)
	}
}

// A personal token acts as its owner: with the owner's current role, and in
// the audit trail under the owner's name, through the token.
func TestPersonalTokenOnTheAPI(t *testing.T) {
	s, database := apiTestServer(t)
	for _, u := range []struct{ name, role string }{{"alice", auth.RoleAdmin}, {"bob", auth.RoleViewer}} {
		if err := auth.CreateUser(database, u.name, "password123", u.role); err != nil {
			t.Fatal(err)
		}
	}
	users, _ := auth.ListUsers(database)
	ids := map[string]int64{}
	for _, u := range users {
		ids[u.Username] = u.ID
	}
	alice, err := auth.CreatePersonalToken(database, ids["alice"], "laptop", 0)
	if err != nil {
		t.Fatal(err)
	}
	bob, err := auth.CreatePersonalToken(database, ids["bob"], "desktop", time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	call := func(token string) (int, string, string) {
		var actor string
		h := s.requireToken(true, func(w http.ResponseWriter, r *http.Request) {
			actor = apiActor(apiIdentity(r))
		})
		req := httptest.NewRequest("POST", "/api/v1/apps/x/deploy", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String(), actor
	}

	if code, _, actor := call(alice); code != http.StatusOK || actor != "alice via laptop" {
		t.Errorf("alice's token: status %d, actor %q", code, actor)
	}
	if code, _, _ := call(bob); code != http.StatusForbidden {
		t.Errorf("a viewer's token on a write route: status %d, want 403", code)
	}
	if _, err := database.Exec("UPDATE api_tokens SET expires_at = ? WHERE name = 'desktop'",
		time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if code, body, _ := call(bob); code != http.StatusUnauthorized || !strings.Contains(body, "expired") {
		t.Errorf("an expired token: status %d, body %q; want 401 saying it expired", code, body)
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
		"Port": true, "State": true, "Uptime": true, "Deploy": true,
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

// GET /me is what a client signs in with: it has to tell a personal token
// from a service one, name the owner, and say when the token expires.
func TestAPIMe(t *testing.T) {
	s, database := apiTestServer(t)
	s.routes()
	if err := auth.CreateUser(database, "alice", "password123", auth.RoleViewer); err != nil {
		t.Fatal(err)
	}
	users, _ := auth.ListUsers(database)
	personal, err := auth.CreatePersonalToken(database, users[0].ID, "laptop", 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	service, err := auth.CreateToken(database, "ci", auth.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}

	get := func(token string) map[string]any {
		t.Helper()
		req := httptest.NewRequest("GET", "/api/v1/me", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		s.mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
		var got map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		return got
	}

	me := get(personal)
	if me["kind"] != "personal" || me["user"] != "alice" || me["token"] != "laptop" || me["role"] != auth.RoleViewer {
		t.Errorf("personal token: %v", me)
	}
	if _, ok := me["expires_at"].(string); !ok {
		t.Errorf("personal token with an expiry reported expires_at %v", me["expires_at"])
	}
	if me["server_version"] == "" || me["server_version"] == nil {
		t.Errorf("no server version: %v", me)
	}

	me = get(service)
	if me["kind"] != "service" || me["user"] != nil || me["role"] != auth.RoleAdmin || me["expires_at"] != nil {
		t.Errorf("service token: %v", me)
	}
}

// Wrong tokens count toward the same lock as wrong passwords: once an address
// is locked out, even a valid token is refused until the lock runs out.
func TestWrongTokensLockTheAddressOut(t *testing.T) {
	s, database := apiTestServer(t)
	s.loginGuard = newLoginGuard(func() loginPolicy { return loginPolicyFrom(database) })
	good, err := auth.CreateToken(database, "ci", auth.RoleViewer)
	if err != nil {
		t.Fatal(err)
	}
	h := s.requireToken(false, func(w http.ResponseWriter, r *http.Request) {})
	call := func(token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/api/v1/apps", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	attempts := loginPolicyFrom(database).Attempts
	for i := 0; i < attempts; i++ {
		if rec := call("qsr_wrong"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status %d, want 401", i+1, rec.Code)
		}
	}
	rec := call(good)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Errorf("after %d wrong tokens a valid one got %d (Retry-After %q), want 429",
			attempts, rec.Code, rec.Header().Get("Retry-After"))
	}
}

// Credentials pasted into a clone URL stay on the server.
func TestAPIAppRedactsGitCredentials(t *testing.T) {
	a := &db.App{ID: "a1", Name: "blog", Subdomain: "blog", DeployType: "git",
		GitURL: "https://user:ghp_secret@github.com/me/blog.git", GitBranch: "main"}
	got := apiAppOf(AppView{App: a})
	if strings.Contains(got.GitURL, "ghp_secret") || strings.Contains(got.GitURL, "user:") {
		t.Errorf("git_url = %q, carries the credentials", got.GitURL)
	}
	if !strings.Contains(got.GitURL, "github.com/me/blog.git") {
		t.Errorf("git_url = %q, lost the repository", got.GitURL)
	}
}

// The deploy state is what a client that started a deploy polls: running,
// then either no error or the reason, with any credentials taken out.
func TestAPIDeployState(t *testing.T) {
	if got := apiDeployOf(nil); got.Running || got.Error != nil {
		t.Errorf("no deploy since start: %+v", got)
	}
	if got := apiDeployOf(&docker.DeployState{Running: true, Err: "stale"}); !got.Running || got.Error != nil {
		t.Errorf("a running deploy reports no error yet: %+v", got)
	}
	got := apiDeployOf(&docker.DeployState{Err: "clone https://u:tok@github.com/x.git: denied"})
	if got.Running || got.Error == nil {
		t.Fatalf("a failed deploy: %+v", got)
	}
	if strings.Contains(*got.Error, "tok@") || !strings.Contains(*got.Error, "denied") {
		t.Errorf("error = %q, want the reason without the credentials", *got.Error)
	}
	if got := apiDeployOf(&docker.DeployState{}); got.Error != nil {
		t.Errorf("a successful deploy reports an error: %+v", got)
	}
}

// Start and stop are admin routes like restart, and answer an unknown
// application with a 404 rather than reaching Docker.
func TestAPILifecycleRoutes(t *testing.T) {
	s, database := apiTestServer(t)
	s.routes()
	admin, _ := auth.CreateToken(database, "ops", auth.RoleAdmin)
	viewer, _ := auth.CreateToken(database, "mon", auth.RoleViewer)
	for _, action := range []string{"start", "stop", "restart"} {
		for _, tc := range []struct {
			token string
			want  int
		}{{viewer, http.StatusForbidden}, {admin, http.StatusNotFound}} {
			req := httptest.NewRequest("POST", "/api/v1/apps/nope/"+action, nil)
			req.Header.Set("Authorization", "Bearer "+tc.token)
			rec := httptest.NewRecorder()
			s.mux.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Errorf("%s: status %d, want %d (%s)", action, rec.Code, tc.want, rec.Body.String())
			}
		}
	}
}
