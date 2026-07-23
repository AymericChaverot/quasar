package server

import (
	"fmt"
	"html"
	"net/http"
	"strings"

	"quasar/internal/db"
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

// handleLogsSearchPartial runs a search over persisted log history, across
// every app or scoped to one, optionally filtered by a substring.
func (s *Server) handleLogsSearchPartial(w http.ResponseWriter, r *http.Request) {
	lines, err := db.SearchLogs(s.db, r.URL.Query().Get("app"), r.URL.Query().Get("q"), logSearchLimit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.renderPartial(w, "logs_results", map[string]any{
		"Lines":     lines,
		"Truncated": len(lines) == logSearchLimit,
	})
}

// handleAppLogs streams container logs as Server-Sent Events, consumed by the
// htmx SSE extension on the app detail page.
func (s *Server) handleAppLogs(w http.ResponseWriter, r *http.Request) {
	a := s.getApp(w, r)
	if a == nil {
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	err := s.dock.StreamLogs(r.Context(), a, func(line string) {
		// One SSE event per log line, HTML-escaped and wrapped for beforeend swap.
		fmt.Fprintf(w, "data: <div>%s</div>\n\n", html.EscapeString(strings.ToValidUTF8(line, "�")))
		flusher.Flush()
	})
	if err != nil && r.Context().Err() == nil {
		fmt.Fprintf(w, "data: <div class=\"text-red-400\">log stream error: %s</div>\n\n", html.EscapeString(err.Error()))
		flusher.Flush()
	}
}

// handleSystemContainerLogs streams a system container's logs the same way
// handleAppLogs does, but by container name and with no mutating routes
// alongside it — this view is read-only.
func (s *Server) handleSystemContainerLogs(w http.ResponseWriter, r *http.Request) {
	sc := s.getSystemContainer(w, r)
	if sc == nil {
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	err := s.dock.StreamLogsByName(r.Context(), sc.Name, func(line string) {
		fmt.Fprintf(w, "data: <div>%s</div>\n\n", html.EscapeString(strings.ToValidUTF8(line, "�")))
		flusher.Flush()
	})
	if err != nil && r.Context().Err() == nil {
		fmt.Fprintf(w, "data: <div class=\"text-red-400\">log stream error: %s</div>\n\n", html.EscapeString(err.Error()))
		flusher.Flush()
	}
}
