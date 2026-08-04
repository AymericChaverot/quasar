package docker

import (
	"fmt"
	"strings"
)

// lineSinkMax bounds one line: a command printing megabytes without ever
// terminating a line is flushed anyway rather than buffered forever.
const lineSinkMax = 8192

// lineSink is the writer a deploy points its subprocesses at. It cuts their
// output into whole lines as they arrive, hands each one on, and keeps the last
// few for the error message if the command turns out to have failed.
//
// Both terminators end a line. git and the compose builder redraw a progress
// line by returning the carriage without a newline, and waiting for the newline
// that only comes at the end would show a whole download as a single line once
// it was already over.
type lineSink struct {
	emit func(string)
	keep int // how many lines to hold for the error message

	buf     strings.Builder
	tail    []string
	dropped int
}

func (s *lineSink) Write(p []byte) (int, error) {
	for _, b := range p {
		if b == '\n' || b == '\r' {
			s.flush()
			continue
		}
		s.buf.WriteByte(b)
		if s.buf.Len() >= lineSinkMax {
			s.flush()
		}
	}
	return len(p), nil
}

// flush ends the line being built, if it holds anything. It is also how the
// caller closes the sink once the command has exited, so that output with no
// final newline is not silently dropped.
func (s *lineSink) flush() {
	line := strings.TrimRight(s.buf.String(), " \t")
	s.buf.Reset()
	if line == "" {
		return
	}
	if s.emit != nil {
		s.emit(line)
	}
	if s.keep <= 0 {
		return
	}
	s.tail = append(s.tail, line)
	if len(s.tail) > s.keep {
		s.tail = append(s.tail[:0], s.tail[1:]...)
		s.dropped++
	}
}

// text renders what was kept, for quoting back in an error. Building a stack
// streams hundreds of progress lines and only says what actually went wrong on
// the last few, so quoting all of it would bury the failure — but truncation
// has to say it happened, or the tail reads as the whole story.
func (s *lineSink) text() string {
	if s.dropped == 0 {
		return strings.Join(s.tail, "\n")
	}
	return fmt.Sprintf("[%d earlier lines omitted]\n%s", s.dropped, strings.Join(s.tail, "\n"))
}
