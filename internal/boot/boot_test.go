package boot

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// Without colour a step is its mark, its name in a column, and the details
// joined; details left empty are dropped rather than joined as blanks.
func TestLinesWithoutColour(t *testing.T) {
	var out bytes.Buffer
	s := &Sequence{out: &out, started: time.Now()}
	s.OK("database", "/data/db.sqlite", "", "3 applications")
	s.Warn("docker", "the daemon did not answer")
	s.Ready("listening on :8080")

	got := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	want := []string{
		"  ✓ database    /data/db.sqlite · 3 applications",
		"  ! docker      the daemon did not answer",
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("line %d = %q, want %q", i, got[i], w)
		}
	}
	if !strings.HasPrefix(got[2], "  ✓ ready       listening on :8080 · started in ") {
		t.Errorf("ready line = %q", got[2])
	}
}

// In colour, the escapes are all closed, so what the log prints next is not
// painted.
func TestLinesInColourEndReset(t *testing.T) {
	var out bytes.Buffer
	s := &Sequence{out: &out, colour: true, started: time.Now()}
	s.OK("config", "domain example.com")
	if !strings.HasSuffix(strings.TrimRight(out.String(), "\n"), reset) {
		t.Errorf("the line does not end reset: %q", out.String())
	}
}
