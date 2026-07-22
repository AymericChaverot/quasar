package server

import (
	"fmt"
	"html"
	"net/http"
	"strings"
)

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
