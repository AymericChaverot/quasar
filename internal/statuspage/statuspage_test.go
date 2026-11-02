package statuspage

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const sampleStatus = `{"title":"Example","link":"https://example.com","updated_at":"2026-11-02T16:40:00Z",
"status":"partial_outage",
"services":[
 {"name":"Cloud","status":"operational","uptime":{"today":100,"7d":99.9,"30d":99.5,"90d":99.24},
  "days":[{"date":"2026-11-01","up":null},{"date":"2026-11-02","up":96.5}]},
 {"name":"<b>Photos</b>","status":"down"}],
"incidents":[{"service":"Cloud","started_at":"2026-11-01T14:02:00Z","ended_at":"2026-11-01T14:19:00Z","duration_seconds":1020,"restarted":true}],
"server":{"cpu":12.3,"memory":61,"disk":88}}`

// The page reads the API with its key and shows what it was given, escaped.
func TestThePageShowsTheStatusItReads(t *testing.T) {
	var gotAuth string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(sampleStatus))
	}))
	defer api.Close()

	rec := httptest.NewRecorder()
	New(Config{API: api.URL, Key: "k"}).ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	body := rec.Body.String()
	if gotAuth != "Bearer k" {
		t.Errorf("the API was asked with %q", gotAuth)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("page answered %d", rec.Code)
	}
	for _, want := range []string{
		"<title>Example</title>", `href="https://example.com"`, "Some services are down",
		"&lt;b&gt;Photos&lt;/b&gt;", "99.24% over 2 days", `class="bar is-none"`, `class="bar is-warn"`,
		"Cloud did not answer", "1 Nov · 14:02 → 14:19 UTC · 17 min", "Restarted automatically.",
		`class="fill is-err" width="88"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	if strings.Contains(body, "<b>Photos</b>") {
		t.Error("a service name reached the page unescaped")
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'none'") {
		t.Errorf("CSP = %q", csp)
	}
}

// An API that does not answer is a page that says so, not an error page.
func TestThePageSaysWhenItCannotReadTheStatus(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusUnauthorized)
	}))
	defer api.Close()
	rec := httptest.NewRecorder()
	New(Config{API: api.URL}).ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "Status unavailable") {
		t.Errorf("unreadable status: %d %s", rec.Code, rec.Body.String())
	}
}

// Only the page, its sheet and a health check are served.
func TestThePageServesNothingElse(t *testing.T) {
	h := New(Config{API: "http://127.0.0.1:1"})
	for path, want := range map[string]int{"/healthz": 204, "/page.css": 200, "/api/v1/status": 404, "/../etc/passwd": 404} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != want {
			t.Errorf("%s answered %d, want %d", path, rec.Code, want)
		}
	}
}

// An ongoing incident says so, and a duration reads in the unit it needs.
func TestIncidentsAreWorded(t *testing.T) {
	in := incidentView(Incident{Service: "Cloud", StartedAt: time.Date(2026, 11, 1, 23, 50, 0, 0, time.UTC), Seconds: 7500, Restarted: true}, time.Now())
	if !in.Ongoing || in.Duration != "2 h 05 min" || !strings.HasPrefix(in.Note, "Ongoing.") {
		t.Errorf("ongoing incident = %+v", in)
	}
	for d, want := range map[time.Duration]string{30 * time.Second: "under a minute", 50 * time.Hour: "2 d 2 h"} {
		if got := duration(d); got != want {
			t.Errorf("duration(%s) = %q, want %q", d, got, want)
		}
	}
}
