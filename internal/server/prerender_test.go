package server

import (
	"bytes"
	"html/template"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"quasar/internal/db"
	"quasar/internal/secrets"
)

// The panels that answer in time are drawn with the page; one that is slow or
// fails is left out, and the page does not wait for the slow one.
func TestPrerenderDrawsWhatAnswersInTime(t *testing.T) {
	s := &Server{mux: http.NewServeMux()}
	s.mux.HandleFunc("GET /partials/fast", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "quasar_session=abc" {
			http.Error(w, "no session", http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, "<p>fast "+r.URL.Query().Get("x")+"</p>")
	})
	s.mux.HandleFunc("GET /partials/slow", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(5 * time.Second):
		case <-r.Context().Done():
		}
		_, _ = io.WriteString(w, "<p>slow</p>")
	})
	s.mux.HandleFunc("GET /partials/broken", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})

	page := httptest.NewRequest("GET", "/", nil)
	page.Header.Set("Cookie", "quasar_session=abc")
	start := time.Now()
	got := s.prerender(page, map[string]string{
		"fast": "/partials/fast?x=1", "slow": "/partials/slow", "broken": "/partials/broken",
	})
	if took := time.Since(start); took > prerenderBudget+time.Second {
		t.Errorf("the page waited %v for its panels", took)
	}
	if got["fast"] != "<p>fast 1</p>" {
		t.Errorf("fast panel = %q", got["fast"])
	}
	if _, ok := got["slow"]; ok {
		t.Error("a panel past the budget was drawn")
	}
	if _, ok := got["broken"]; ok {
		t.Error("a failed panel was drawn")
	}
}

// A dashboard drawn with its panels does not fetch them again on load, and
// one drawn without them does, behind its placeholders.
func TestDashboardDrawsPrerenderedPanels(t *testing.T) {
	s := testServer(t)
	render := func(pre map[string]template.HTML) string {
		var buf bytes.Buffer
		data := map[string]any{"IsAdmin": true}
		if pre != nil {
			data["Pre"] = pre
		}
		if err := s.pages["dashboard"].ExecuteTemplate(&buf, "content", data); err != nil {
			t.Fatal(err)
		}
		return buf.String()
	}

	with := render(map[string]template.HTML{"apps": `<table id="drawn-apps"></table>`})
	if !strings.Contains(with, `<table id="drawn-apps"></table>`) {
		t.Error("the drawn panel is not in the page")
	}
	apps := with[strings.Index(with, `id="apps-pane"`):]
	apps = apps[:strings.Index(apps, ">")]
	if strings.Contains(apps, "load,") {
		t.Errorf("a drawn panel still fetches itself on load: %s", apps)
	}
	if !strings.Contains(with, `hx-get="/partials/system" hx-trigger="load,`) {
		t.Error("a panel that was not drawn no longer fetches itself")
	}

	without := render(nil)
	if strings.Count(without, `hx-trigger="load,`) != 4 || !strings.Contains(without, "animate-pulse") {
		t.Error("without its panels the dashboard does not fall back to placeholders")
	}
}

// The Logs page arrives with its first page of lines in it, fetched through
// the same route and session the page's own search uses.
func TestLogsPageArrivesWithItsLines(t *testing.T) {
	s, r, _ := signedIn(t)
	s.guards, s.mux = map[string]string{}, http.NewServeMux()
	s.routes()
	k, err := secrets.LoadOrCreateKey(filepath.Join(t.TempDir(), "master.key"))
	if err != nil {
		t.Fatal(err)
	}
	s.keyring = k
	if err := db.InsertApp(s.db, k, &db.App{ID: "a1", Name: "Web", Subdomain: "web", DeployType: "image", ImageRef: "nginx"}); err != nil {
		t.Fatal(err)
	}
	if err := db.AppendLogs(s.db, "a1", []db.LogEntry{{Line: "hello from the first paint"}}); err != nil {
		t.Fatal(err)
	}

	page := httptest.NewRequest("GET", "/logs?app=a1", nil)
	for _, c := range r.Cookies() {
		page.AddCookie(c)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, page)
	body := w.Body.String()
	if w.Code != 200 || !strings.Contains(body, "hello from the first paint") {
		t.Fatalf("the page came without its lines (%d)", w.Code)
	}
	results := body[strings.Index(body, `id="log-results"`):]
	if tag := results[:strings.Index(results, ">")]; strings.Contains(tag, "hx-trigger") {
		t.Errorf("drawn results still fetch themselves: %s", tag)
	}
}

// The environment panel replaces itself when it polls, so a drawn one goes in
// as it came — its own wrapper — and not inside the placeholder that would
// fetch it again.
func TestSystemPageDrawsEnvironmentWhole(t *testing.T) {
	s := testServer(t)
	var buf bytes.Buffer
	data := map[string]any{
		"Title": "System", "IsAdmin": true,
		"Pre": map[string]template.HTML{"environment": `<div id="system-env">drawn</div>`},
	}
	if err := s.pages["system"].ExecuteTemplate(&buf, "content", data); err != nil {
		t.Fatal(err)
	}
	html := buf.String()
	if !strings.Contains(html, `<div id="system-env">drawn</div>`) {
		t.Error("the drawn environment is not in the page")
	}
	if strings.Contains(html, `hx-get="/partials/system/environment"`) {
		t.Error("a drawn environment is still fetched again")
	}
	if !strings.Contains(html, `hx-get="/partials/system/certs" hx-trigger="load"`) {
		t.Error("a panel that was not drawn no longer fetches itself")
	}
}
