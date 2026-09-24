// Package event is the dashboard's log of what happens on the platform while
// it runs: an application deployed or failing to, an update, a backup, a
// sign-in, a certificate dropped. One line each, in the same shape as the
// start-up sequence — a mark, the area in a column, then what happened:
//
//	✓ deploy      portfolio · deployed in 41s · ghcr.io/me/portfolio:1.2 · by admin
//	✗ backup      scheduled · disk full
//	! login       3 failed attempts for "admin" from 203.0.113.7
//
// It is not the audit trail. The audit is every change, kept in the database
// and searched from its page; this is what an operator reading `docker logs`
// needs in order to know how the platform is doing. Settings saved, pages
// viewed and panels polled are not here.
//
// Lines carry no time of their own: Quasar's log pane shows the time Docker
// recorded for each, and `docker logs -t` does the same. A second one in the
// text was only ever the same time twice.
package event

import (
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"time"
)

// Level is how a line is marked.
type Level int

const (
	// OK is something that went as asked.
	OK Level = iota
	// Warn is something worth a look that did not stop anything.
	Warn
	// Fail is something that did not happen, or broke.
	Fail
)

const (
	reset = "\x1b[0m"
	green = "\x1b[38;2;52;211;153m"  // --ok
	amber = "\x1b[38;2;251;191;36m"  // --warn
	red   = "\x1b[38;2;248;113;113m" // --err
	text  = "\x1b[38;2;232;233;236m" // --text
	muted = "\x1b[38;2;150;152;160m" // --text-muted
)

// AreaWidth lines the details up in a column. The start-up sequence uses the
// same, so the two read as one log.
const AreaWidth = 12

var (
	mu     sync.Mutex
	out    io.Writer = os.Stderr
	colour           = os.Getenv("NO_COLOR") == ""
)

// Info records something that went as asked.
func Info(area string, details ...string) { Print(OK, area, details...) }

// Warning records something worth a look that did not stop anything.
func Warning(area string, details ...string) { Print(Warn, area, details...) }

// Error records something that did not happen, or broke.
func Error(area string, details ...string) { Print(Fail, area, details...) }

// Print writes one line: details are joined with " · ", and empty ones left
// out, so a caller can pass a detail it may not have.
func Print(level Level, area string, details ...string) {
	var kept []string
	for _, d := range details {
		if d = strings.TrimSpace(d); d != "" {
			kept = append(kept, d)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	_, _ = io.WriteString(out, Format(level, area, strings.Join(kept, " · "), colour))
}

// Duration writes how long something took as precisely as is worth reading:
// milliseconds under a second, where "0s" would say nothing, tenths under ten
// seconds, whole seconds past that.
func Duration(d time.Duration) string {
	switch {
	case d < time.Millisecond:
		return "under 1ms"
	case d < time.Second:
		return d.Round(time.Millisecond).String()
	case d < 10*time.Second:
		return d.Round(100 * time.Millisecond).String()
	}
	return d.Round(time.Second).String()
}

// Format is one line as Print writes it, for the start-up sequence to share.
func Format(level Level, area, detail string, inColour bool) string {
	mark, c := "✓", green
	switch level {
	case Warn:
		mark, c = "!", amber
	case Fail:
		mark, c = "✗", red
	}
	pad := strings.Repeat(" ", max(1, AreaWidth-len(area)))
	if !inColour {
		return "  " + mark + " " + area + pad + detail + "\n"
	}
	return "  " + c + mark + reset + " " + text + area + reset + pad + muted + detail + reset + "\n"
}

// CaptureStandardLog routes the standard log package through here, so the
// errors code throughout the dashboard reports with log.Printf come out in
// the same shape, as failures, without their timestamp.
//
// Those messages start with where they come from — "monitor: recording the
// sample: …" — which becomes the area. One that does not is filed under its
// first word.
func CaptureStandardLog() {
	log.SetFlags(0)
	log.SetOutput(stdLog{})
}

type stdLog struct{}

func (stdLog) Write(p []byte) (int, error) {
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		area, detail := splitArea(line)
		Print(Fail, area, detail)
	}
	return len(p), nil
}

// maxArea is how long an "area: message" prefix may be and still be taken as
// the area; past it, the colon is part of the message.
const maxArea = 20

func splitArea(line string) (area, detail string) {
	if i := strings.Index(line, ": "); i > 0 && i <= maxArea {
		return line[:i], line[i+2:]
	}
	if i := strings.IndexByte(line, ' '); i > 0 {
		return line[:i], line[i+1:]
	}
	return "quasar", line
}
