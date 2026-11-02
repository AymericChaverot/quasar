package server

import (
	"html"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"quasar/internal/db"
	"quasar/internal/docker"
)

// logPageSize is how many lines the Logs page shows at a time.
const logPageSize = 100

// logContextLines is how many lines are shown on each side of one whose
// context is asked for, and how many more each press of Earlier or Later adds.
const logContextLines = 15

// logContextMax is the most lines asked for on one side, however many times
// Earlier is pressed: past that, the application's own Logs tab is the place.
const logContextMax = 300

// LogPeriod is one of the spans on the page's period switch.
type LogPeriod struct{ Value, Label string }

// logPeriodChoices are the period switch, in its order. "" is every line.
var logPeriodChoices = []LogPeriod{{"1h", "Last hour"}, {"24h", "24 h"}, {"7d", "7 days"}, {"", "All"}}

// logPeriods are the spans the Logs page can be narrowed to, as the address
// names them.
var logPeriods = map[string]time.Duration{
	"1h":  time.Hour,
	"24h": 24 * time.Hour,
	"7d":  7 * 24 * time.Hour,
}

// logSearch is a search of the Logs page, as its address carries it.
type logSearch struct {
	App    string
	Text   string
	Exact  bool
	Since  string // a key of logPeriods, "" for every line
	Errors bool
}

func logSearchFrom(r *http.Request) logSearch {
	q := r.URL.Query()
	s := logSearch{App: q.Get("app"), Text: q.Get("q"), Exact: q.Get("exact") == "1", Errors: q.Get("errors") == "1"}
	if _, ok := logPeriods[q.Get("since")]; ok {
		s.Since = q.Get("since")
	}
	return s
}

// values is the search as the address writes it, leaving out what is not set.
func (s logSearch) values() url.Values {
	v := url.Values{}
	set := func(k, val string) {
		if val != "" {
			v.Set(k, val)
		}
	}
	set("app", s.App)
	set("q", s.Text)
	set("since", s.Since)
	if s.Exact {
		v.Set("exact", "1")
	}
	if s.Errors {
		v.Set("errors", "1")
	}
	return v
}

// query is the search for the database, its period counted back from now.
func (s logSearch) query() db.LogQuery {
	q := db.LogQuery{App: s.App, Text: s.Text, Exact: s.Exact, Errors: s.Errors}
	if d, ok := logPeriods[s.Since]; ok {
		q.Since = time.Now().Add(-d)
	}
	return q
}

// key is what a count of the search is kept under.
func (s logSearch) key() string { return s.values().Encode() }

// handleLogsPage renders the cross-app log search page.
func (s *Server) handleLogsPage(w http.ResponseWriter, r *http.Request) {
	apps, _ := db.ListApps(s.db, s.keyring)
	search, page := logSearchFrom(r), pageOf(r)
	v := search.values()
	v.Set("page", strconv.Itoa(page))
	results := "/partials/logs?" + v.Encode()
	s.render(w, r, "logs", map[string]any{
		"Title":   "Logs",
		"Apps":    apps,
		"Search":  search,
		"Periods": logPeriodChoices,
		"Page":    page,
		"Results": results,
		// The page of lines, drawn with the page rather than after it: a
		// search over every stored line that is slow to answer is the one
		// case left to arrive on its own.
		"Pre": s.prerender(r, map[string]string{"results": results}),
	})
}

// LogLineView is a stored log line with its text already rendered, so history
// reads the same as the live pane rather than showing the raw escape sequences
// the container wrote.
type LogLineView struct {
	db.LogLine
	HTML template.HTML
	// Focus is the line the context around it was asked for.
	Focus bool
}

// handleLogsSearchPartial runs a search over persisted log history, across
// every app or scoped to one, a page at a time.
//
// Only the page on screen is read. How many pages there are takes counting
// every match — a whole table, for a search — so the page does not wait for
// it: it is drawn with a pager that knows whether there is an older page,
// and the full one follows in a request of its own (handleLogsPagerPartial).
// When there is no older page the total is already known, and a recent count
// of the same search is reused, so most pages need no counting at all.
func (s *Server) handleLogsSearchPartial(w http.ResponseWriter, r *http.Request) {
	search := logSearchFrom(r)
	query := search.values()
	page := pageOf(r)
	// One line more than a page, to know whether there is a page after it.
	lines, err := db.SearchLogs(s.db, search.query(), logPageSize+1, (page-1)*logPageSize)
	if err == nil && len(lines) == 0 && page > 1 {
		// A page past the end — typed in, or left behind by lines that aged
		// out — is the last one, which is the one case that counts first.
		total, cerr := s.countLogs(search)
		if cerr != nil {
			internalError(w, r, cerr)
			return
		}
		page = pageCount(total, logPageSize)
		lines, err = db.SearchLogs(s.db, search.query(), logPageSize+1, (page-1)*logPageSize)
	}
	if err != nil {
		internalError(w, r, err)
		return
	}
	more := len(lines) > logPageSize
	lines = lines[:min(len(lines), logPageSize)]

	var pager Pager
	// Total is how many lines the search matches, -1 until it is counted.
	total, known := s.logCounts.get(search.key())
	switch {
	case !more:
		// The last page: everything before it was full.
		total = (page-1)*logPageSize + len(lines)
		s.logCounts.set(search.key(), total)
		pager = pagerFor("/logs", query, page, pageCount(total, logPageSize))
	case known:
		// A count from moments ago; lines have been written since, so it is
		// never allowed to say this page is the last.
		pager = pagerFor("/logs", query, page, max(pageCount(total, logPageSize), page+1))
	default:
		total = -1
		pager = provisionalPager("/logs", query, page)
		pager.Pending = pageURL("/partials/logs/pager", query, page)
	}
	marks := markPattern(search.query().Highlights(), search.Exact)
	views := make([]LogLineView, 0, len(lines))
	for _, l := range lines {
		views = append(views, LogLineView{LogLine: l, HTML: renderLogLineMarked(l.Line, marks)})
	}
	if r.Header.Get("HX-Request") != "" {
		followURL(w, r, "log-search", pageURL("/logs", query, page))
	}
	s.renderPartial(w, "logs_results", map[string]any{
		"Lines": views,
		"Page":  page,
		"Total": total,
		"Query": query.Encode(),
		"Pager": pager.withPartial("/partials/logs", query, "#log-results"),
	})
}

// handleLogsContextPartial is the lines an application wrote around one of
// its lines: what was going on when an error was logged. The line is marked
// as the search marked it, so it is found again among the others.
func (s *Server) handleLogsContextPartial(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "no such line", http.StatusNotFound)
		return
	}
	side := func(name string) int {
		n, err := strconv.Atoi(r.URL.Query().Get(name))
		if err != nil || n < 0 {
			return logContextLines
		}
		return min(n, logContextMax)
	}
	before, after := side("before"), side("after")
	lines, err := db.LogContext(s.db, id, before, after)
	if err != nil {
		internalError(w, r, err)
		return
	}
	search := logSearchFrom(r)
	marks := markPattern(search.query().Highlights(), search.Exact)
	views := make([]LogLineView, 0, len(lines))
	for _, l := range lines {
		views = append(views, LogLineView{LogLine: l, HTML: renderLogLineMarked(l.Line, marks), Focus: l.ID == id})
	}
	// Earlier and Later are offered while there may be more on their side:
	// a side that came back short has reached the first or last line stored.
	gotBefore, gotAfter := 0, 0
	for i, v := range views {
		if v.Focus {
			gotBefore, gotAfter = i, len(views)-i-1
		}
	}
	data := map[string]any{"ID": id, "Lines": views, "Query": search.values().Encode(), "Before": before, "After": after}
	if gotBefore == before && before < logContextMax {
		data["Earlier"] = min(before+logContextLines, logContextMax)
	}
	if gotAfter == after && after < logContextMax {
		data["Later"] = min(after+logContextLines, logContextMax)
	}
	if len(views) > 0 {
		data["App"], data["AppName"] = views[0].AppID, views[0].AppName
	}
	s.renderPartial(w, "logs_context", data)
}

// handleLogsPagerPartial is the full pager for a page of the Logs page, which
// fetches it once it is on screen: counting the matches is what it costs.
func (s *Server) handleLogsPagerPartial(w http.ResponseWriter, r *http.Request) {
	search := logSearchFrom(r)
	total, err := s.countLogs(search)
	if err != nil {
		internalError(w, r, err)
		return
	}
	page := pageOf(r)
	query := search.values()
	pager := pagerFor("/logs", query, page, max(pageCount(total, logPageSize), page))
	s.renderPartial(w, "logs_pager_counted", map[string]any{
		"Pager": pager.withPartial("/partials/logs", query, "#log-results"),
		"Total": total,
	})
}

// countLogs is how many lines a search matches, from a count made moments ago
// if there is one.
func (s *Server) countLogs(search logSearch) (int, error) {
	key := search.key()
	if n, ok := s.logCounts.get(key); ok {
		return n, nil
	}
	n, err := db.CountLogs(s.db, search.query())
	if err != nil {
		return 0, err
	}
	s.logCounts.set(key, n)
	return n, nil
}

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

// forget drops every count, for when the lines they counted are gone.
func (c *countCache) forget() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m = nil
}
