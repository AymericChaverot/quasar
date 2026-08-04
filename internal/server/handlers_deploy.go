package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"quasar/internal/docker"
)

// deployLogKeepalive is how long a quiet deploy stream waits before writing a
// comment down it. A deploy can spend minutes inside one build step without a
// word, and a proxy between the browser and here will close a connection it
// believes has gone idle.
const deployLogKeepalive = 25 * time.Second

// deployState is the progress half of the stream: where the deploy has got to,
// rather than what it printed getting there.
type deployState struct {
	// Active reports whether there has been a deploy this boot at all, which is
	// what decides whether the panel is on screen.
	Active   bool    `json:"active"`
	Running  bool    `json:"running"`
	Phase    string  `json:"phase"`
	Percent  float64 `json:"percent"`
	Measured bool    `json:"measured"`
	Error    string  `json:"error"`
}

// handleAppDeployLog streams a deploy as it runs: the output of the clone, the
// build and the compose command as each line arrives, and the step and
// percentage alongside them.
//
// It is admin-only, unlike the container log pane beside it. What a container
// prints is the application talking; what a build prints is the Dockerfile
// running, which routinely echoes build arguments and the environment compose
// interpolated its file with — the same secrets the environment editor on this
// page is already withheld from viewers for.
func (s *Server) handleAppDeployLog(w http.ResponseWriter, r *http.Request) {
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

	// This connection has seen nothing, so the first snapshot redraws the pane
	// whole — whether it was opened during a deploy, long after one, or before
	// the app has ever had any.
	gen, seq := int64(-1), -1
	for {
		snap, changed := s.dock.WatchDeploy(a.ID, gen, seq)
		if snap.Reset {
			fmt.Fprint(w, "event: reset\ndata: \n\n")
		}
		for _, l := range snap.Lines {
			fmt.Fprintf(w, "event: line\ndata: %s\n\n", renderDeployLine(l))
		}
		state, err := json.Marshal(deployState{
			Active:   snap.Gen > 0,
			Running:  snap.Running,
			Phase:    snap.Phase,
			Percent:  snap.Percent,
			Measured: snap.Measured,
			Error:    snap.Err,
		})
		if err != nil {
			return
		}
		fmt.Fprintf(w, "event: state\ndata: %s\n\n", state)
		flusher.Flush()
		gen, seq = snap.Gen, snap.Seq

		select {
		case <-changed:
		case <-r.Context().Done():
			return
		case <-time.After(deployLogKeepalive):
			fmt.Fprint(w, ": still here\n\n")
			flusher.Flush()
		}
	}
}

// renderDeployLine renders one line of a deploy for the stream.
//
// Quasar's own commentary is marked so the pane can set it apart from what the
// build printed. An SSE frame is newline-delimited, so the result must carry
// none of its own or it would split into two events and truncate the line.
func renderDeployLine(l docker.DeployLine) string {
	open := "<div>"
	if l.Note {
		open = `<div class="log-note">`
	}
	return open + strings.ReplaceAll(string(renderLogLine(l.Text)), "\n", " ") + "</div>"
}
