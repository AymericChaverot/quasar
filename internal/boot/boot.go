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
	"io"
	"os"
	"strings"
	"time"

	"quasar/internal/event"
)

const (
	reset = "\x1b[0m"
	label = "\x1b[38;2;232;233;236m" // --text
	muted = "\x1b[38;2;150;152;160m" // --text-muted
	// faint is for what a line only qualifies — where a setting came from.
	faint = "\x1b[38;2;105;108;119m" // --text-faint
)

const (
	// labelWidth lines the details up in a column, the same one the events
	// logged afterwards use.
	labelWidth = event.AreaWidth
	// settingWidth lines setting values up in a column of their own.
	settingWidth = 16
)

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
func (s *Sequence) OK(name string, details ...string) { s.line(event.OK, name, details) }

// Warn says a step came up short of what it should, but the dashboard goes on
// without it: Docker unreachable, a migration that did not run.
func (s *Sequence) Warn(name string, details ...string) { s.line(event.Warn, name, details) }

// Fatal says a step failed in a way the dashboard cannot start past, and
// stops it.
func (s *Sequence) Fatal(name string, err error) {
	s.line(event.Fail, name, []string{err.Error()})
	os.Exit(1)
}

// Ready closes the sequence, with how long it took.
func (s *Sequence) Ready(details ...string) {
	took := time.Since(s.started).Round(time.Millisecond)
	s.line(event.OK, "ready", append(details, "started in "+took.String()))
	s.write("\n")
}

// Setting writes one loaded setting under the step before it: its name, its
// value, and a note on where it came from. An empty value is written as such,
// so an unset variable is seen to be unset — and without the note, which has
// nothing to add to it.
func (s *Sequence) Setting(name, value, note string) {
	if value == "" {
		value, note = "(not set)", ""
	}
	indent := strings.Repeat(" ", 4+labelWidth)
	pad := strings.Repeat(" ", max(1, settingWidth-len(name)))
	if note != "" {
		note = "  " + note
	}
	if !s.colour {
		s.write(indent + name + pad + value + note + "\n")
		return
	}
	s.write(indent + muted + name + reset + pad + label + value + faint + note + reset + "\n")
}

func (s *Sequence) line(level event.Level, name string, details []string) {
	var kept []string
	for _, d := range details {
		if d != "" {
			kept = append(kept, d)
		}
	}
	s.write(event.Format(level, name, strings.Join(kept, " · "), s.colour))
}

// write puts a line out. A start-up log that cannot be written has nowhere
// to report that either, and the dashboard starts regardless.
func (s *Sequence) write(line string) {
	_, _ = io.WriteString(s.out, line)
}
