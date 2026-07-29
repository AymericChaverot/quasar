package server

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

const esc = "\x1b"

// The line that prompted this: Traefik colours its own output, and those
// sequences used to reach the browser as literal "[90m" wrapped around every
// field.
func TestRenderHonoursTraefikColour(t *testing.T) {
	line := esc + "[90m2026-07-28T23:31:01Z" + esc + "[0m " +
		esc + "[33mWRN" + esc + "[0m " +
		esc + "[1mA new release of Traefik has been found: 3.7.9." + esc + "[0m"

	got := string(renderLogLine(line))

	if strings.Contains(got, "[90m") || strings.Contains(got, esc) {
		t.Errorf("escape sequences leaked into the output:\n%s", got)
	}
	for _, want := range []string{
		`class="ansi-bright-black"`, // the dimmed timestamp
		`class="ansi-yellow"`,       // WRN
		`class="ansi-bold"`,         // the message
		"A new release of Traefik has been found: 3.7.9.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output does not carry %s:\n%s", want, got)
		}
	}
	// The program said yellow, so Quasar must not also paint the line red for
	// containing "WRN" — its own guess would override what the program chose.
	if strings.Contains(got, "log-warn") {
		t.Errorf("a self-coloured line was also given an inferred colour:\n%s", got)
	}
}

// A plain line gets colour from its severity word, which is the whole point of
// the automatic flags.
func TestRenderInfersLevelWhenUncoloured(t *testing.T) {
	tests := []struct {
		line string
		want string
	}{
		{"2026-07-28 23:31:01 ERROR could not connect to database", "log-err"},
		{"panic: runtime error: index out of range", "log-err"},
		{"time=... level=warn msg=\"disk almost full\"", "log-warn"},
		{"[INFO] listening on :8080", "log-info"},
		{"DEBUG cache miss for key foo", "log-debug"},
		{`{"level":"fatal","msg":"boom"}`, "log-err"},
	}
	for _, tc := range tests {
		t.Run(tc.want+" "+tc.line[:12], func(t *testing.T) {
			if got := string(renderLogLine(tc.line)); !strings.Contains(got, tc.want) {
				t.Errorf("renderLogLine(%q) = %s, want class %s", tc.line, got, tc.want)
			}
		})
	}
}

// Severity words live near the front of a line in every log format. Scanning
// the whole line would paint an ordinary message red for mentioning the word.
func TestRenderDoesNotInferFromProse(t *testing.T) {
	uncoloured := []string{
		"GET /index.html 200 12ms",
		"connection established, replication caught up with no errors reported at all",
		"user signed in",
	}
	for _, line := range uncoloured {
		got := string(renderLogLine(line))
		for _, class := range []string{"log-err", "log-warn", "log-info", "log-debug"} {
			if strings.Contains(got, class) {
				t.Errorf("renderLogLine(%q) applied %s, want no colour", line, class)
			}
		}
	}
}

// ownTags matches the markup renderLogLine is allowed to emit. Stripping it
// leaves nothing but escaped log text, which is what the invariant below
// checks.
var ownTags = regexp.MustCompile(`</?(?:span|time)(?: [^<>]*)?>`)

// Log text is bytes off a container's stdout, which is attacker-controlled for
// any app someone chose to deploy. This is the only place it becomes markup,
// so the invariant is absolute: after the tags this function itself writes,
// not one character of the line may still be able to act as markup.
func TestRenderEscapesLogText(t *testing.T) {
	hostile := []string{
		`<script>alert(1)</script>`,
		`" onmouseover="alert(1)`,
		esc + `[31m<img src=x onerror=alert(1)>` + esc + `[0m`,
		`</span><script>alert(1)</script>`,
		esc + `[38;5;1m"><script>alert(1)</script>`,
		`plain & simple`,
	}
	for _, line := range hostile {
		got := string(renderLogEntry(time.Now(), line))
		if rest := ownTags.ReplaceAllString(got, ""); strings.ContainsAny(rest, `<>"`) {
			t.Errorf("renderLogEntry(%q) left live markup characters in %q\nfull output: %s", line, rest, got)
		}
	}
}

// Cursor movement, erase-line and window-title sequences mean nothing in a
// scrolling pane and must not be printed as text.
func TestRenderDropsNonColourSequences(t *testing.T) {
	line := esc + "[2K" + esc + "[1G" + esc + "]0;window title" + "\x07" + "building..."
	got := string(renderLogLine(line))
	if got != "building..." {
		t.Errorf("renderLogLine() = %q, want the control sequences gone", got)
	}
}

// Progress bars redraw one line with carriage returns; only the last draw is
// worth keeping.
func TestRenderKeepsTheLastCarriageReturnDraw(t *testing.T) {
	if got := string(renderLogLine("downloading 10%\rdownloading 80%\rdownloading 100%")); got != "downloading 100%" {
		t.Errorf("renderLogLine() = %q, want only the final draw", got)
	}
}

// 256-colour and truecolor cannot be themed through a class, so they arrive as
// an inline colour — and their arguments must be consumed, or they would be
// read as further attributes.
func TestRenderExtendedColours(t *testing.T) {
	got := string(renderLogLine(esc + "[38;5;208morange" + esc + "[0m"))
	if !strings.Contains(got, "color:#ff8700") {
		t.Errorf("256-colour not resolved: %s", got)
	}

	got = string(renderLogLine(esc + "[38;2;18;52;86mrgb" + esc + "[0m"))
	if !strings.Contains(got, "color:#123456") {
		t.Errorf("truecolor not resolved: %s", got)
	}

	// A background is dropped, but stepping over its arguments matters: read as
	// attributes, the trailing "1" would turn the text bold.
	got = string(renderLogLine(esc + "[48;5;1mplain" + esc + "[0m"))
	if strings.Contains(got, "ansi-bold") || strings.Contains(got, "ansi-red") {
		t.Errorf("background arguments were read as attributes: %s", got)
	}
}

// The pane is read for what just happened, so it shows the clock; the full
// instant stays available without cluttering every line.
func TestRenderLogEntryStamp(t *testing.T) {
	ts := time.Date(2026, 7, 28, 23, 31, 1, 500_000_000, time.UTC)
	got := string(renderLogEntry(ts, "hello"))

	if !strings.Contains(got, `datetime="2026-07-28T23:31:01.5Z"`) {
		t.Errorf("machine-readable timestamp missing:\n%s", got)
	}
	if !strings.Contains(got, `title="2026-07-28 23:31:01.500 UTC"`) {
		t.Errorf("full instant missing from the title:\n%s", got)
	}
	if !strings.Contains(got, ts.Local().Format("15:04:05")) {
		t.Errorf("clock missing:\n%s", got)
	}
}

// A line the daemon gave no timestamp for must render without one, not under
// the zero time — "0001-01-01" on every line of a stream would be worse than
// no stamp at all.
func TestRenderLogEntryWithoutTimestamp(t *testing.T) {
	got := string(renderLogEntry(time.Time{}, "hello"))
	if strings.Contains(got, "log-ts") || strings.Contains(got, "0001") {
		t.Errorf("renderLogEntry(zero) = %q, want a bare line", got)
	}
	if got != "hello" {
		t.Errorf("renderLogEntry(zero) = %q, want %q", got, "hello")
	}
}
