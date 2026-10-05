package server

import (
	"html"
	"html/template"
	"net/http"
	"strings"
	"sync"
	"time"

	"quasar/internal/db"
	"quasar/internal/docker"
)

// logPageSize is how many lines the Logs page shows at a time.
const logPageSize = 100

// handleLogsPage renders the cross-app log search page.
func (s *Server) handleLogsPage(w http.ResponseWriter, r *http.Request) {
	apps, _ := db.ListApps(s.db, s.keyring)
	s.render(w, r, "logs", map[string]any{
		"Title": "Logs",
		"Apps":  apps,
		"App":   r.URL.Query().Get("app"),
		"Query": r.URL.Query().Get("q"),
		"Page":  pageOf(r),
	})
}

// LogLineView is a stored log line with its text already rendered, so history
// reads the same as the live pane rather than showing the raw escape sequences
// the container wrote.
type LogLineView struct {
	db.LogLine
	HTML template.HTML
}

// handleLogsSearchPartial runs a search over persisted log history, across
// every app or scoped to one, optionally filtered by a substring, a page at a
// time.
//
// Only the page on screen is read. How many pages there are takes counting
// every match — a whole table, for a search — so the page does not wait for
// it: it is drawn with a pager that knows whether there is an older page,
// and the full one follows in a request of its own (handleLogsPagerPartial).
// When there is no older page the total is already known, and a recent count
// of the same search is reused, so most pages need no counting at all.
func (s *Server) handleLogsSearchPartial(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	app, q := query.Get("app"), query.Get("q")
	page := pageOf(r)
	// One line more than a page, to know whether there is a page after it.
	lines, err := db.SearchLogs(s.db, app, q, logPageSize+1, (page-1)*logPageSize)
	if err == nil && len(lines) == 0 && page > 1 {
		// A page past the end — typed in, or left behind by lines that aged
		// out — is the last one, which is the one case that counts first.
		total, cerr := s.countLogs(app, q)
		if cerr != nil {
			http.Error(w, cerr.Error(), http.StatusInternalServerError)
			return
		}
		page = pageCount(total, logPageSize)
		lines, err = db.SearchLogs(s.db, app, q, logPageSize+1, (page-1)*logPageSize)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	more := len(lines) > logPageSize
	lines = lines[:min(len(lines), logPageSize)]

	var pager Pager
	switch total, known := s.logCounts.get(logCountKey(app, q)); {
	case !more:
		// The last page: everything before it was full.
		total = (page-1)*logPageSize + len(lines)
		s.logCounts.set(logCountKey(app, q), total)
		pager = pagerFor("/logs", query, page, pageCount(total, logPageSize))
	case known:
		// A count from moments ago; lines have been written since, so it is
		// never allowed to say this page is the last.
		pager = pagerFor("/logs", query, page, max(pageCount(total, logPageSize), page+1))
	default:
		pager = provisionalPager("/logs", query, page)
		pager.Pending = pageURL("/partials/logs/pager", query, page)
	}
	views := make([]LogLineView, 0, len(lines))
	for _, l := range lines {
		views = append(views, LogLineView{LogLine: l, HTML: renderLogLine(l.Line)})
	}
	// The address bar follows the results — a new search as much as another
	// page — so a reload shows what is on screen. Replaced rather than
	// pushed: stepping through pages is not worth a history entry each.
	if r.Header.Get("HX-Request") != "" {
		w.Header().Set("HX-Replace-Url", pageURL("/logs", query, page))
	}
	s.renderPartial(w, "logs_results", map[string]any{
		"Lines": views,
		"Page":  page,
		"Pager": pager.withPartial("/partials/logs", query, "#log-results"),
	})
}

// handleLogsPagerPartial is the full pager for a page of the Logs page, which
// fetches it once it is on screen: counting the matches is what it costs.
func (s *Server) handleLogsPagerPartial(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	total, err := s.countLogs(query.Get("app"), query.Get("q"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	page := pageOf(r)
	pager := pagerFor("/logs", query, page, max(pageCount(total, logPageSize), page))
	s.renderPartial(w, "logs_pager", pager.withPartial("/partials/logs", query, "#log-results"))
}

// countLogs is how many lines a search matches, from a count made moments ago
// if there is one.
func (s *Server) countLogs(app, q string) (int, error) {
	key := logCountKey(app, q)
	if n, ok := s.logCounts.get(key); ok {
		return n, nil
	}
	n, err := db.CountLogs(s.db, app, q)
	if err != nil {
		return 0, err
	}
	s.logCounts.set(key, n)
	return n, nil
}

func logCountKey(app, q string) string { return app + "\x00" + q }

// countFresh is how long a count is reused. Long enough to step through a
// few pages of one search without counting it again; short enough that the
// number of pages is never far behind what is being written.
const countFresh = 20 * time.Second

// countCache keeps recent counts of searches.
type countCache struct {
	mu sync.Mutex
	m  map[string]countEntry
}

type countEntry struct {
	n  int
	at time.Time
}

func (c *countCache) get(key string) (int, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[key]
	if !ok || time.Since(e.at) > countFresh {
		return 0, false
	}
	return e.n, true
}

func (c *countCache) set(key string, n int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]countEntry{}
	}
	// Searches are typed, so keys pile up; the stale ones go when a new one
	// comes in rather than on a timer of their own.
	for k, e := range c.m {
		if time.Since(e.at) > countFresh {
			delete(c.m, k)
		}
	}
	c.m[key] = countEntry{n, time.Now()}
}

// streamLogLines writes a log stream as Server-Sent Events, consumed by the
// htmx SSE extension in the log pane. follow is handed a sink to call once per
// line and blocks until the request is cancelled.
func streamLogLines(w http.ResponseWriter, r *http.Request, follow func(send func(docker.LogLine)) error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	err := follow(func(l docker.LogLine) {
		// One SSE event per log line, wrapped for beforeend swap. An SSE frame
		// is newline-delimited, so a rendered line must not carry one — it
		// would split into two events and truncate the line.
		rendered := strings.ReplaceAll(string(renderLogEntry(l.TS, l.Text)), "\n", " ")
		if !sse(w, "data: %s%s</div>\n\n", lineOpen(l.Text), rendered) {
			return
		}
		flusher.Flush()
	})
	// A cancelled request is the normal way this ends — the reader navigated
	// away — and has no error to report to a response nobody is reading.
	if err != nil && r.Context().Err() == nil {
		if sse(w, "data: <div class=\"text-red-400\">log stream error: %s</div>\n\n", html.EscapeString(err.Error())) {
			flusher.Flush()
		}
	}
}

// handleAppLogs streams the app's logs: its container, or the service a stack
// serves HTTP from.
func (s *Server) handleAppLogs(w http.ResponseWriter, r *http.Request) {
	a := s.getApp(w, r)
	if a == nil {
		return
	}
	streamLogLines(w, r, func(send func(docker.LogLine)) error {
		return s.dock.StreamLogs(r.Context(), a, send)
	})
}

// handleAppContainerLogs streams one container of a stack, which is the only
// way to read a service that is not the one serving HTTP.
func (s *Server) handleAppContainerLogs(w http.ResponseWriter, r *http.Request) {
	a := s.getApp(w, r)
	if a == nil {
		return
	}
	ac := s.getAppContainer(w, r, a)
	if ac == nil {
		return
	}
	streamLogLines(w, r, func(send func(docker.LogLine)) error {
		return s.dock.StreamLogsByName(r.Context(), ac.Name, send)
	})
}

// handleSystemContainerLogs streams one of Quasar's own containers, from the
// read-only system view.
func (s *Server) handleSystemContainerLogs(w http.ResponseWriter, r *http.Request) {
	sc := s.getSystemContainer(w, r)
	if sc == nil {
		return
	}
	streamLogLines(w, r, func(send func(docker.LogLine)) error {
		return s.dock.StreamLogsByName(r.Context(), sc.Name, send)
	})
}
