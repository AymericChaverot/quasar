package server

import (
	"fmt"
	"html"
	"html/template"
	"net/http"
	"strings"

	"quasar/internal/db"
	"quasar/internal/docker"
)

// logSearchLimit caps how many matching lines a single search returns.
const logSearchLimit = 300

// handleLogsPage renders the cross-app log search page.
func (s *Server) handleLogsPage(w http.ResponseWriter, r *http.Request) {
	apps, _ := db.ListApps(s.db, s.keyring)
	s.render(w, r, "logs", map[string]any{
		"Title": "Logs",
		"Apps":  apps,
		"App":   r.URL.Query().Get("app"),
		"Query": r.URL.Query().Get("q"),
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
// every app or scoped to one, optionally filtered by a substring.
func (s *Server) handleLogsSearchPartial(w http.ResponseWriter, r *http.Request) {
	lines, err := db.SearchLogs(s.db, r.URL.Query().Get("app"), r.URL.Query().Get("q"), logSearchLimit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	views := make([]LogLineView, 0, len(lines))
	for _, l := range lines {
		views = append(views, LogLineView{LogLine: l, HTML: renderLogLine(l.Line)})
	}
	s.renderPartial(w, "logs_results", map[string]any{
		"Lines":     views,
		"Truncated": len(lines) == logSearchLimit,
	})
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
		fmt.Fprintf(w, "data: <div>%s</div>\n\n", rendered)
		flusher.Flush()
	})
	// A cancelled request is the normal way this ends — the reader navigated
	// away — and has no error to report to a response nobody is reading.
	if err != nil && r.Context().Err() == nil {
		fmt.Fprintf(w, "data: <div class=\"text-red-400\">log stream error: %s</div>\n\n", html.EscapeString(err.Error()))
		flusher.Flush()
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
