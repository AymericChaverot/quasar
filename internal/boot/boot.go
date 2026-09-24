// Package boot writes the dashboard's start-up sequence under its banner: one
// line per thing brought up, saying what it found — the database and how many
// applications are in it, which Docker it reached, where it will listen.
//
// It is what someone reads in `docker logs quasar-dashboard` after an update
// or a restart, to see at a glance that everything came up, and on which
// versions. So each line is short and says something checkable; nothing here
// is repeated while the dashboard runs.
package boot

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

const (
	reset = "\x1b[0m"
	ok    = "\x1b[38;2;52;211;153m"  // --ok
	warn  = "\x1b[38;2;251;191;36m"  // --warn
	fail  = "\x1b[38;2;248;113;113m" // --err
	label = "\x1b[38;2;232;233;236m" // --text
	muted = "\x1b[38;2;150;152;160m" // --text-muted
)

// labelWidth lines the details up in a column.
const labelWidth = 12

// Sequence is one start-up, written to out as it goes.
type Sequence struct {
	out     io.Writer
	colour  bool
	started time.Time
}

// Start begins the sequence on stderr, where log writes too. NO_COLOR
// (no-color.org) asks for it without colour.
func Start() *Sequence {
	return &Sequence{out: os.Stderr, colour: os.Getenv("NO_COLOR") == "", started: time.Now()}
}

// OK says a step came up, and what it found.
func (s *Sequence) OK(name string, details ...string) { s.line("✓", ok, name, details) }

// Warn says a step came up short of what it should, but the dashboard goes on
// without it: Docker unreachable, a migration that did not run.
func (s *Sequence) Warn(name string, details ...string) { s.line("!", warn, name, details) }

// Fatal says a step failed in a way the dashboard cannot start past, and
// stops it.
func (s *Sequence) Fatal(name string, err error) {
	s.line("✗", fail, name, []string{err.Error()})
	os.Exit(1)
}

// Ready closes the sequence, with how long it took.
func (s *Sequence) Ready(details ...string) {
	took := time.Since(s.started).Round(time.Millisecond)
	s.line("✓", ok, "ready", append(details, "started in "+took.String()))
	fmt.Fprintln(s.out)
}

func (s *Sequence) line(mark, colour, name string, details []string) {
	var kept []string
	for _, d := range details {
		if d != "" {
			kept = append(kept, d)
		}
	}
	detail := strings.Join(kept, " · ")
	pad := strings.Repeat(" ", max(1, labelWidth-len(name)))
	if !s.colour {
		fmt.Fprintf(s.out, "  %s %s%s%s\n", mark, name, pad, detail)
		return
	}
	fmt.Fprintf(s.out, "  %s%s%s %s%s%s%s%s%s\n", colour, mark, reset, label, name, reset, pad, muted+detail, reset)
}
