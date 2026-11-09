package server

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"quasar/internal/db"
)

// folderUpload is a gzipped tar holding the given files, as qsr sends one.
func folderUpload(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func uploadCall(s *Server, appID, token string, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest("PUT", "/api/v1/apps/"+appID+"/source", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/gzip")
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	return rec
}

// localApp adds an application deployed from a local folder to the server.
func localApp(t *testing.T, s *Server) *db.App {
	t.Helper()
	a := &db.App{ID: "l1", Name: "site", Subdomain: "site", DeployType: "local", Port: 80}
	if err := db.InsertApp(s.db, s.keyring, a); err != nil {
		t.Fatal(err)
	}
	return a
}

// waitDeploy waits for the deploy an upload started to end, and returns its
// error.
func waitDeploy(t *testing.T, s *Server, appID string) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if d := s.dock.Deploying(appID); d != nil && !d.Running {
			return d.Err
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the deploy did not end")
	return ""
}

// An upload replaces the source and starts a deploy of it. This one has
// nothing to build, so the deploy fails without reaching Docker, saying why.
func TestAPIUpload(t *testing.T) {
	s, appsDir, tokens := envTestServer(t)
	a := localApp(t, s)

	rec := uploadCall(s, a.ID, tokens["alice"], folderUpload(t, map[string]string{"index.html": "<h1>hi</h1>"}))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["files"] != float64(1) || got["status"] != "deploying" {
		t.Errorf("answered %v", got)
	}
	if b, _ := os.ReadFile(filepath.Join(appsDir, a.ID, "source", "index.html")); string(b) != "<h1>hi</h1>" {
		t.Errorf("source file = %q", b)
	}
	if msg := waitDeploy(t, s, a.ID); !strings.Contains(msg, "neither a Dockerfile nor a compose file") {
		t.Errorf("deploy error = %q", msg)
	}
	if !auditHas(t, s, "app.upload", "alice via laptop") {
		t.Error("the upload should be audited against the token")
	}
}

func TestAPIUploadRefusals(t *testing.T) {
	s, appsDir, tokens := envTestServer(t)
	a := localApp(t, s)
	src := filepath.Join(appsDir, a.ID, "source")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "Dockerfile"), []byte("FROM nginx"), 0o644); err != nil {
		t.Fatal(err)
	}
	good := folderUpload(t, map[string]string{"Dockerfile": "FROM caddy"})

	if rec := uploadCall(s, a.ID, tokens["bob"], good); rec.Code != http.StatusForbidden {
		t.Errorf("viewer: status %d", rec.Code)
	}
	if rec := uploadCall(s, "nope", tokens["alice"], good); rec.Code != http.StatusNotFound {
		t.Errorf("unknown app: status %d", rec.Code)
	}
	// blog, from envTestServer, runs an image.
	if rec := uploadCall(s, "a1", tokens["alice"], good); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "an image") {
		t.Errorf("image app: status %d %s", rec.Code, rec.Body.String())
	}
	if rec := uploadCall(s, a.ID, tokens["alice"], []byte("not an archive")); rec.Code != http.StatusBadRequest {
		t.Errorf("not an archive: status %d %s", rec.Code, rec.Body.String())
	}
	if rec := uploadCall(s, a.ID, tokens["alice"], folderUpload(t, map[string]string{"../escape": "x"})); rec.Code != http.StatusBadRequest {
		t.Errorf("escaping path: status %d %s", rec.Code, rec.Body.String())
	}

	if err := db.SetSetting(s.db, db.SettingUploadLimitMB, "1"); err != nil {
		t.Fatal(err)
	}
	big := make([]byte, 2<<20)
	rec := uploadCall(s, a.ID, tokens["alice"], big)
	if rec.Code != http.StatusRequestEntityTooLarge || !strings.Contains(rec.Body.String(), "1 MB") {
		t.Errorf("too large: status %d %s", rec.Code, rec.Body.String())
	}

	if b, _ := os.ReadFile(filepath.Join(src, "Dockerfile")); string(b) != "FROM nginx" {
		t.Errorf("a refused upload changed the source: %q", b)
	}
	if d := s.dock.Deploying(a.ID); d != nil {
		t.Error("a refused upload must not deploy")
	}
}

// The limit comes from Settings, falls back to the default when unset or out
// of range, and is what /me reports.
func TestUploadLimitSetting(t *testing.T) {
	s, _, tokens := envTestServer(t)
	if got := s.uploadLimitMB(); got != defaultUploadLimitMB {
		t.Errorf("unset: %d", got)
	}
	for stored, want := range map[string]int{"50": 50, "0": defaultUploadLimitMB, "999999": defaultUploadLimitMB, "x": defaultUploadLimitMB} {
		if err := db.SetSetting(s.db, db.SettingUploadLimitMB, stored); err != nil {
			t.Fatal(err)
		}
		if got := s.uploadLimitMB(); got != want {
			t.Errorf("stored %q: %d, want %d", stored, got, want)
		}
	}
	if err := db.SetSetting(s.db, db.SettingUploadLimitMB, "50"); err != nil {
		t.Fatal(err)
	}
	rec := apiCall(s, "GET", "/api/v1/me", tokens["bob"], "")
	var me map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &me); err != nil {
		t.Fatal(err)
	}
	if me["upload_max_bytes"] != float64(50<<20) {
		t.Errorf("/me upload_max_bytes = %v", me["upload_max_bytes"])
	}
}

// A client that does not say how large its upload is still gets it refused
// once it goes past the limit, with the same answer.
func TestAPIUploadTooLargeWithoutALength(t *testing.T) {
	s, _, tokens := envTestServer(t)
	a := localApp(t, s)
	if err := db.SetSetting(s.db, db.SettingUploadLimitMB, "1"); err != nil {
		t.Fatal(err)
	}
	noise := make([]byte, 3<<20)
	if _, err := rand.Read(noise); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("PUT", "/api/v1/apps/"+a.ID+"/source",
		bytes.NewReader(folderUpload(t, map[string]string{"noise.bin": string(noise)})))
	req.ContentLength = -1
	req.Header.Set("Authorization", "Bearer "+tokens["alice"])
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge || !strings.Contains(rec.Body.String(), "1 MB") {
		t.Errorf("status %d %s", rec.Code, rec.Body.String())
	}
}
