package server

import (
	"net/http/httptest"
	"strings"
	"testing"

	"quasar/internal/config"
	"quasar/internal/db"
)

// A share is of the whole machine: CPU is counted a hundred per core, and a
// figure that is not nothing still draws a sliver.
func TestAnApplicationsBarsAreAgainstTheMachine(t *testing.T) {
	r := appResources(db.AppUsage{CPU: 200, MemMB: 2048}, 4, 8192)
	if r.CPUShare != 50 || r.MemShare != 25 || r.CPU != "200.0%" || r.Mem != "2.0 GB" {
		t.Errorf("two cores of four and 2 GB of 8: %+v", r)
	}
	if r := appResources(db.AppUsage{CPU: 0.1, MemMB: 12}, 16, 65536); r.CPUShare != 2 || r.Mem != "12 MB" {
		t.Errorf("a whisper of use should still show a sliver: %+v", r)
	}
	if r := appResources(db.AppUsage{CPU: 900}, 4, 0); r.CPUShare != 100 || r.MemShare != 0 {
		t.Errorf("over the machine is a full bar, and an unknown memory size draws none: %+v", r)
	}
}

// A day's line is scaled to its own highest point and spans the box.
func TestASparklineSpansItsBox(t *testing.T) {
	pts := []db.MetricPoint{{V1: 0}, {V1: 5}, {V1: 10}}
	got := sparkline(pts, func(p db.MetricPoint) float64 { return p.V1 })
	if got != "0.0,13.0 24.0,7.0 48.0,1.0" {
		t.Errorf("sparkline = %q", got)
	}
}

// The display is read from the picker and written back only when it changes,
// then from the cookie, then the bars.
func TestTheResourceDisplayIsRemembered(t *testing.T) {
	s := &Server{cfg: config.Config{}}
	rec := httptest.NewRecorder()
	if got := s.resourceMode(rec, httptest.NewRequest("GET", "/partials/apps", nil)); got != "bars" {
		t.Errorf("default = %q, want bars", got)
	}
	rec = httptest.NewRecorder()
	if got := s.resourceMode(rec, httptest.NewRequest("GET", "/partials/apps?resources=columns", nil)); got != "columns" {
		t.Errorf("picked = %q", got)
	}
	if c := rec.Result().Cookies(); len(c) != 1 || c[0].Value != "columns" {
		t.Fatalf("cookies = %v; want the choice written", c)
	}

	poll := httptest.NewRequest("GET", "/partials/apps?resources=columns", nil)
	poll.Header.Set("Cookie", resourcesCookie+"=columns")
	rec = httptest.NewRecorder()
	s.resourceMode(rec, poll)
	if c := rec.Result().Cookies(); len(c) != 0 {
		t.Errorf("a poll repeating the saved choice wrote %v", c)
	}

	odd := httptest.NewRequest("GET", "/partials/apps?resources=nonsense", nil)
	odd.Header.Set("Cookie", resourcesCookie+"=off")
	if got := s.resourceMode(httptest.NewRecorder(), odd); got != "off" {
		t.Errorf("an unknown value should fall back to the saved one, got %q", got)
	}
}

// The bars and columns render the figures, and Hidden leaves the list as it
// was.
func TestTheListDrawsResourcesAsChosen(t *testing.T) {
	s := testServer(t)
	row := AppView{App: &db.App{ID: "a1", Name: "Blog", Subdomain: "blog", DeployType: "image", ImageRef: "nginx"},
		Domain: "example.com", Usage: &AppResources{CPU: "6.2%", Mem: "1.8 GB", CPUShare: 4, MemShare: 12, CPULine: "0,5 48,9"}}
	render := func(mode string) string {
		var b strings.Builder
		if err := s.pages["dashboard"].ExecuteTemplate(&b, "apps_table", AppsTable{Rows: []AppView{row}, Resources: mode}); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	if bars := render("bars"); !strings.Contains(bars, ">Resources<") || !strings.Contains(bars, `width="12"`) {
		t.Error("the bars mode should draw a Resources column with the memory's share")
	}
	if cols := render("columns"); !strings.Contains(cols, ">CPU<") || !strings.Contains(cols, `points="0,5 48,9"`) {
		t.Error("the columns mode should draw CPU and RAM with their lines")
	}
	if off := render("off"); strings.Contains(off, "col-resources") || strings.Contains(off, "1.8 GB") {
		t.Error("hidden should draw no resources at all")
	}
}
