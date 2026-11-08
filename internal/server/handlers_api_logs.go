package server

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"time"

	"quasar/internal/db"
	"quasar/internal/docker"
)

// The bounds of ?tail=. The default is what the dashboard's log pane opens
// with; the ceiling keeps one call from asking Docker for a container's whole
// history, which is what the Logs page's stored lines are for.
const (
	apiLogTailDefault = 200
	apiLogTailMax     = 5000
)

// apiLogLine is one line of output. Time is when Docker received it, and null
// when Docker gave none.
type apiLogLine struct {
	Time *time.Time `json:"time"`
	Line string     `json:"line"`
}

// handleAPILogs answers an application's recent output, and with follow=true
// keeps answering as it writes more, until the caller hangs up.
//
// The body is newline-delimited JSON, one object per line of output, so a
// client can act on each line as it arrives and needs no framing beyond the
// newline. Each line is flushed as it comes when following.
//
// The lines are the ones the dashboard shows on the application's page —
// which a viewer may read — so any token may read them too.
func (s *Server) handleAPILogs(w http.ResponseWriter, r *http.Request) {
	a, err := db.GetApp(s.db, s.keyring, r.PathValue("id"))
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "application not found")
		return
	}
	tail := apiLogTailDefault
	if v := r.URL.Query().Get("tail"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > apiLogTailMax {
			writeAPIError(w, http.StatusBadRequest, "tail must be a number from 1 to "+strconv.Itoa(apiLogTailMax))
			return
		}
		tail = n
	}
	follow := false
	if v := r.URL.Query().Get("follow"); v != "" {
		follow, err = strconv.ParseBool(v)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "follow must be true or false")
			return
		}
	}

	flusher, _ := w.(http.Flusher)
	enc := json.NewEncoder(w)
	started := false
	start := func() {
		started = true
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
	}
	// A followed stream answers as soon as Docker has accepted it, so the
	// caller knows it is connected before the application next writes
	// anything. Not before: a missing container still gets a proper error.
	ready := func() {
		if follow {
			start()
			if flusher != nil {
				flusher.Flush()
			}
		}
	}
	opts := docker.LogOptions{Tail: tail, Follow: follow, Ready: ready}
	err = s.dock.ReadLogs(r.Context(), a, opts, func(l docker.LogLine) {
		if !started {
			start()
		}
		out := apiLogLine{Line: l.Text}
		if !l.TS.IsZero() {
			ts := l.TS.UTC()
			out.Time = &ts
		}
		if err := enc.Encode(out); err != nil {
			return // the caller is gone; the cancelled context ends the read
		}
		if follow && flusher != nil {
			flusher.Flush()
		}
	})
	switch {
	case err == nil:
		if !started {
			start() // no lines at all: an empty body, which is an empty log
		}
	case r.Context().Err() != nil:
		// The caller hung up, which is how a followed stream normally ends.
	case !started:
		log.Printf("api: logs of %s: %v", a.Name, err)
		writeAPIError(w, http.StatusConflict, "no logs to read: "+docker.RedactURL(err.Error()))
	default:
		// Halfway through a body there is no status left to change.
		log.Printf("api: logs of %s: %v", a.Name, err)
	}
}
