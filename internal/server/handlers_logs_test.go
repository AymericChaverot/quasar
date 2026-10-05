package server

import (
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"quasar/internal/db"
	"quasar/internal/secrets"
)

// logHistory is a server whose one application has written n lines, the
// newest numbered n.
func logHistory(t *testing.T, n int) *Server {
	t.Helper()
	s, database := catalogTestServer(t)
	k, err := secrets.LoadOrCreateKey(filepath.Join(t.TempDir(), "master.key"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.InsertApp(database, k, &db.App{ID: "a1", Name: "Web", Subdomain: "web", DeployType: "image", ImageRef: "nginx"}); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	var lines []db.LogEntry
	for i := 1; i <= n; i++ {
		lines = append(lines, db.LogEntry{TS: start.Add(time.Duration(i) * time.Second), Line: fmt.Sprintf("line-%04d", i)})
	}
	if err := db.AppendLogs(database, "a1", lines); err != nil {
		t.Fatal(err)
	}
	return s
}

func searchLogs(s *Server, target string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", target, nil)
	r.Header.Set("HX-Request", "true")
	w := httptest.NewRecorder()
	s.handleLogsSearchPartial(w, r)
	return w
}

// A page holds logPageSize lines and links to the next; the page after it
// starts where it stopped, and the last one links no further.
func TestLogsSearchPages(t *testing.T) {
	s := logHistory(t, logPageSize+5)

	first := searchLogs(s, "/partials/logs?app=a1").Body.String()
	if !strings.Contains(first, fmt.Sprintf("line-%04d", logPageSize+5)) || strings.Contains(first, "line-0005") {
		t.Error("the first page does not hold the newest lines, and only those")
	}
	if !strings.Contains(first, `hx-get="/partials/logs?app=a1&amp;page=2"`) {
		t.Error("the first page does not link to the second")
	}

	w := searchLogs(s, "/partials/logs?app=a1&page=2")
	second := w.Body.String()
	if !strings.Contains(second, "line-0005") || strings.Contains(second, "line-0006") {
		t.Error("the second page does not hold exactly the five oldest lines")
	}
	if strings.Contains(second, "Older") && !strings.Contains(second, `aria-disabled="true">Older`) {
		t.Error("the last page links to an older one")
	}
	// A reload shows the page on screen.
	if got := w.Header().Get("HX-Replace-Url"); got != "/logs?app=a1&page=2" {
		t.Errorf("HX-Replace-Url = %q", got)
	}
}

// One page is all there is: no pager at all.
func TestLogsSearchOnePageHasNoPager(t *testing.T) {
	s := logHistory(t, 3)
	if body := searchLogs(s, "/partials/logs").Body.String(); strings.Contains(body, "pager") {
		t.Error("a single page of lines draws a pager")
	}
}

// Each line's application is named in its own colour.
func TestLogsSearchColoursTheApplication(t *testing.T) {
	s := logHistory(t, 1)
	body := searchLogs(s, "/partials/logs").Body.String()
	if !strings.Contains(body, `class="log-app hover:underline" style="--app: #`) {
		t.Errorf("the application is not drawn in its colour:\n%s", body)
	}
}

// A page of logs does not wait for its search to be counted: it comes with a
// pager that knows there is an older page, and that fetches the full one once
// it is on screen. The full one says how many pages there are.
func TestLogsPageDoesNotWaitForTheCount(t *testing.T) {
	s := logHistory(t, 3*logPageSize+5)

	first := searchLogs(s, "/partials/logs?app=a1").Body.String()
	if !strings.Contains(first, `hx-get="/partials/logs/pager?app=a1"`) || !strings.Contains(first, `hx-trigger="load"`) {
		t.Fatalf("the first page does not fetch its pager:\n%s", first)
	}
	if !strings.Contains(first, `hx-get="/partials/logs?app=a1&amp;page=2"`) {
		t.Error("the provisional pager does not link to the next page")
	}
	if strings.Contains(first, `max="`) {
		t.Error("the provisional pager claims to know the last page")
	}

	r := httptest.NewRequest("GET", "/partials/logs/pager?app=a1", nil)
	w := httptest.NewRecorder()
	s.handleLogsPagerPartial(w, r)
	full := w.Body.String()
	if !strings.Contains(full, `max="4"`) || strings.Contains(full, "hx-trigger") {
		t.Errorf("the full pager does not say there are 4 pages:\n%s", full)
	}

	// Counted moments ago, the next page needs no counting of its own.
	second := searchLogs(s, "/partials/logs?app=a1&page=2").Body.String()
	if strings.Contains(second, "/partials/logs/pager") || !strings.Contains(second, `max="4"`) {
		t.Errorf("a recent count was not reused:\n%s", second)
	}
}

// The last page knows the total without counting: everything before it was a
// full page.
func TestTheLastPageOfLogsKnowsTheTotal(t *testing.T) {
	s := logHistory(t, 2*logPageSize+5)
	last := searchLogs(s, "/partials/logs?app=a1&page=3").Body.String()
	if strings.Contains(last, "/partials/logs/pager") || !strings.Contains(last, `max="3"`) {
		t.Errorf("the last page counted, or got the total wrong:\n%s", last)
	}
	// A page past the end is the last one.
	w := searchLogs(s, "/partials/logs?app=a1&page=9")
	if !strings.Contains(w.Body.String(), "line-0005") || w.Header().Get("HX-Replace-Url") != "/logs?app=a1&page=3" {
		t.Errorf("a page past the end did not land on the last: %s", w.Header().Get("HX-Replace-Url"))
	}
}
