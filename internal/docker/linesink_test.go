package docker

import (
	"fmt"
	"strings"
	"testing"
)

// git redraws its progress by returning the carriage without a newline. Waiting
// for a newline that only comes at the end would show the whole transfer as one
// line once it was already over.
func TestLineSinkEndsALineOnEitherTerminator(t *testing.T) {
	var got []string
	s := &lineSink{emit: func(l string) { got = append(got, l) }}

	s.Write([]byte("Cloning into 'source'...\nReceiving objects:  40%\r"))
	s.Write([]byte("Receiving objects: 100%\r\nResolving deltas"))
	s.flush()

	want := []string{
		"Cloning into 'source'...",
		"Receiving objects:  40%",
		"Receiving objects: 100%",
		"Resolving deltas",
	}
	if len(got) != len(want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// A stack build streams hundreds of progress lines and only says what actually
// went wrong on the last few, so the error quotes the tail — but truncation has
// to say it happened, or the tail reads as the whole story.
func TestLineSinkTailSaysWhatItDropped(t *testing.T) {
	s := &lineSink{keep: 3}
	for i := 0; i < 10; i++ {
		fmt.Fprintf(s, "line %d\n", i)
	}

	got := s.text()
	if !strings.Contains(got, "line 9") {
		t.Errorf("the last line was dropped:\n%s", got)
	}
	if !strings.Contains(got, "7 earlier lines omitted") {
		t.Errorf("truncation went unannounced:\n%s", got)
	}
	if strings.Contains(got, "line 6") {
		t.Errorf("kept more than it was asked to:\n%s", got)
	}
}

func TestLineSinkShortOutputComesThroughWhole(t *testing.T) {
	s := &lineSink{keep: 20}
	s.Write([]byte("no configuration file provided: not found\n"))
	if got, want := s.text(), "no configuration file provided: not found"; got != want {
		t.Errorf("text() = %q, want %q", got, want)
	}
}

// A command that prints megabytes without ever ending a line must not be
// buffered forever.
func TestLineSinkFlushesAnEndlessLine(t *testing.T) {
	var got []string
	s := &lineSink{emit: func(l string) { got = append(got, l) }}
	s.Write([]byte(strings.Repeat("x", lineSinkMax*2)))
	if len(got) != 2 {
		t.Fatalf("emitted %d lines, want the run cut into 2", len(got))
	}
	for i, l := range got {
		if len(l) != lineSinkMax {
			t.Errorf("line %d is %d bytes, want %d", i, len(l), lineSinkMax)
		}
	}
}
