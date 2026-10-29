package server

import (
	"bytes"
	"html/template"
	"net/http/httptest"
	"strings"
	"testing"

	"quasar/internal/db"
	"quasar/internal/docker"
)

// A template that fails halfway sends nothing of itself: a 500, not a 200
// carrying the half it got through, which htmx would swap in.
func TestExecuteSendsNothingOfAFailedRender(t *testing.T) {
	tmpl := template.Must(template.New("x").Parse(`<div>first half{{.Missing.Field}}</div>`))
	w := httptest.NewRecorder()
	if err := execute(w, tmpl, "x", map[string]any{"Missing": 1}); err == nil {
		t.Fatal("a failing template reported no error")
	}
	if w.Code != 500 || strings.Contains(w.Body.String(), "first half") {
		t.Errorf("got %d %q, want a 500 with none of the page", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	ok := template.Must(template.New("y").Parse(`<p>{{.}}</p>`))
	if err := execute(w, ok, "y", "hi"); err != nil || w.Body.String() != "<p>hi</p>" {
		t.Errorf("a good render: %v %q", err, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type %q", ct)
	}
}

// An empty dashboard offers the first deploy to an admin and not to a viewer,
// who would only be refused it.
func TestEmptyAppListOffersDeployToAdminsOnly(t *testing.T) {
	s := testServer(t)
	for admin, want := range map[bool]bool{true: true, false: false} {
		var buf bytes.Buffer
		if err := s.pages["dashboard"].ExecuteTemplate(&buf, "apps_empty", admin); err != nil {
			t.Fatal(err)
		}
		if got := strings.Contains(buf.String(), `href="/apps/new"`); got != want {
			t.Errorf("admin=%v: link shown %v, want %v", admin, got, want)
		}
	}
	var buf bytes.Buffer
	if err := s.pages["dashboard"].ExecuteTemplate(&buf, "content", map[string]any{"IsAdmin": false}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), `href="/apps/new"`) {
		t.Error("the dashboard offers a viewer New application")
	}
}

// The status panel offers what the state allows, and nothing while a deploy
// runs.
func TestStatusPanelOffersWhatTheStateAllows(t *testing.T) {
	s := testServer(t)
	for state, want := range map[string]struct{ start, stop, disabled bool }{
		"running":      {start: false, stop: true},
		"stopped":      {start: true, stop: false},
		"error":        {start: true, stop: true},
		"not deployed": {start: false, stop: false},
		"deploying":    {start: false, stop: true, disabled: true},
	} {
		v := AppView{
			App:     &db.App{ID: "a1", Name: "Blog", DeployType: "image", ImageRef: "nginx"},
			Status:  docker.AppStatus{State: state},
			IsAdmin: true,
		}
		var buf bytes.Buffer
		if err := s.pages["dashboard"].ExecuteTemplate(&buf, "app_status_panel", v); err != nil {
			t.Fatal(err)
		}
		html := buf.String()
		if got := strings.Contains(html, `/apps/a1/start"`); got != want.start {
			t.Errorf("%s: Start shown %v, want %v", state, got, want.start)
		}
		if got := strings.Contains(html, `/apps/a1/stop"`); got != want.stop {
			t.Errorf("%s: Stop shown %v, want %v", state, got, want.stop)
		}
		if got := strings.Contains(html, " disabled>"); got != want.disabled {
			t.Errorf("%s: buttons disabled %v, want %v", state, got, want.disabled)
		}
		if !strings.Contains(html, `/apps/a1/redeploy"`) || !strings.Contains(html, `hx-sync="#status-panel:replace"`) {
			t.Errorf("%s: Redeploy missing, or not queued on the panel", state)
		}
	}
}
