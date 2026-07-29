package server

import (
	"fmt"
	"html"
	"html/template"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Rendering a log line has two jobs, and which one applies is decided by the
// line itself.
//
// A program that already colours its output — Traefik, most Go and Rust
// loggers, anything using zerolog — sends ANSI escape sequences. Those used to
// reach the browser as literal "[90m" noise wrapped around every field, which
// is worse than no colour at all. Where they are present they are honoured, and
// nothing is invented on top.
//
// A program that prints plain text gets colour derived from its severity word,
// which is the only signal available and is nearly universal across log
// formats. That inference never runs on a line that coloured itself: the
// program's own choices win over a guess about them.

// csiRe matches an ANSI control sequence. Only the ones ending in "m" carry
// colour; the rest move the cursor or erase, mean nothing in a scrolling pane,
// and are dropped rather than printed.
var csiRe = regexp.MustCompile("\x1b\\[[0-9;?]*[ -/]*[@-~]")

// escapeRe matches the other escape sequences a terminal program emits — the
// window title an entrypoint script sets, character-set selection — which are
// likewise dropped.
var escapeRe = regexp.MustCompile("\x1b\\][^\x07\x1b]*(?:\x07|\x1b\\\\)?|\x1b[()][0-9A-Za-z]|\x1b[=>]")

// levelRules colour a line that carries no ANSI of its own, most severe first
// so "error" wins over the "info" further along the same line.
var levelRules = []struct {
	class string
	re    *regexp.Regexp
}{
	{"log-err", regexp.MustCompile(`(?i)\b(error|erro|err|fatal|ftl|panic|critical|crit|emerg|alert)\b`)},
	{"log-warn", regexp.MustCompile(`(?i)\b(warning|warn|wrn)\b`)},
	{"log-info", regexp.MustCompile(`(?i)\b(info|inf|notice)\b`)},
	{"log-debug", regexp.MustCompile(`(?i)\b(debug|dbg|trace|trc)\b`)},
}

// levelScanLimit is how far into a line a severity word is looked for. Every
// log format puts it near the front, next to the timestamp, and stopping there
// is what keeps the word "error" inside a message — "connected, no errors" —
// from painting the whole line red.
const levelScanLimit = 64

// renderLogEntry renders a line behind the time its container wrote it.
//
// The clock alone is shown — a pane is read for what just happened, and the
// date repeated on every line is noise — with the full instant, to the
// nanosecond and in UTC, on the element's title for when it is not. A line the
// daemon gave no timestamp for is rendered without one rather than under the
// zero time.
func renderLogEntry(ts time.Time, line string) template.HTML {
	body := renderLogLine(line)
	if ts.IsZero() {
		return body
	}
	stamp := `<time class="log-ts" datetime="` + html.EscapeString(ts.UTC().Format(time.RFC3339Nano)) +
		`" title="` + html.EscapeString(ts.UTC().Format("2006-01-02 15:04:05.000 MST")) + `">` +
		ts.Local().Format("15:04:05") + `</time> `
	return template.HTML(stamp) + body
}

// renderLogLine converts one raw log line into the HTML the pane displays.
//
// It is the only place log text becomes markup, so it is also the only place
// that has to escape it: every caller hands it bytes straight off a container's
// stdout, which is attacker-controlled for any app a user chose to deploy.
func renderLogLine(line string) template.HTML {
	line = strings.ToValidUTF8(line, "�")
	// Carriage returns from progress-bar output would otherwise leave a stray
	// glyph; a lone \r means "redraw this line", and the last draw is the one
	// worth keeping.
	if i := strings.LastIndexByte(strings.TrimSuffix(line, "\r"), '\r'); i >= 0 {
		line = line[i+1:]
	}
	line = escapeRe.ReplaceAllString(line, "")

	var b strings.Builder
	var style sgrStyle
	write := func(text string) {
		// Any escape byte left after the sequence matchers is a malformed one.
		// It carries no meaning and must not reach the browser as a raw control
		// character, which html.EscapeString would happily pass through.
		text = strings.ReplaceAll(text, "\x1b", "")
		if text == "" {
			return
		}
		if attr := style.attr(); attr != "" {
			b.WriteString("<span " + attr + ">")
			b.WriteString(html.EscapeString(text))
			b.WriteString("</span>")
			return
		}
		b.WriteString(html.EscapeString(text))
	}

	last := 0
	coloured := false
	for _, m := range csiRe.FindAllStringIndex(line, -1) {
		write(line[last:m[0]])
		last = m[1]
		seq := line[m[0]:m[1]]
		if strings.HasSuffix(seq, "m") {
			style.apply(seq[2 : len(seq)-1])
			coloured = true
		}
	}
	write(line[last:])

	out := b.String()
	if coloured {
		return template.HTML(out)
	}
	if class := detectLevel(line); class != "" {
		return template.HTML(`<span class="` + class + `">` + out + `</span>`)
	}
	return template.HTML(out)
}

// detectLevel returns the CSS class for a plain line's severity, or "" when it
// names none.
func detectLevel(line string) string {
	head := line
	if len(head) > levelScanLimit {
		head = head[:levelScanLimit]
	}
	for _, rule := range levelRules {
		if rule.re.MatchString(head) {
			return rule.class
		}
	}
	return ""
}

// sgrStyle is the drawing state an ANSI stream builds up: colour and attributes
// stay in force until something changes them, so this persists across segments
// of one line.
type sgrStyle struct {
	fgClass string // one of the 16 basic colours, as a CSS class
	fgHex   string // a 256-colour or truecolor value, as #rrggbb
	bold    bool
	dim     bool
	italic  bool
	under   bool
}

// basicColours maps the ANSI foreground codes to class names. Backgrounds
// (40-47, 100-107) are parsed and dropped: the pane has one deliberate
// background, and a program painting its own over it tends to produce
// something unreadable against a theme it never saw.
var basicColours = map[int]string{
	30: "black", 31: "red", 32: "green", 33: "yellow",
	34: "blue", 35: "magenta", 36: "cyan", 37: "white",
	90: "bright-black", 91: "bright-red", 92: "bright-green", 93: "bright-yellow",
	94: "bright-blue", 95: "bright-magenta", 96: "bright-cyan", 97: "bright-white",
}

// apply folds one SGR parameter list into the current style.
func (s *sgrStyle) apply(params string) {
	// "ESC[m" is shorthand for "ESC[0m".
	if params == "" {
		*s = sgrStyle{}
		return
	}
	fields := strings.Split(params, ";")
	for i := 0; i < len(fields); i++ {
		n, err := strconv.Atoi(fields[i])
		if err != nil {
			continue
		}
		switch {
		case n == 0:
			*s = sgrStyle{}
		case n == 1:
			s.bold = true
		case n == 2:
			s.dim = true
		case n == 3:
			s.italic = true
		case n == 4:
			s.under = true
		case n == 22:
			s.bold, s.dim = false, false
		case n == 23:
			s.italic = false
		case n == 24:
			s.under = false
		case n == 39:
			s.fgClass, s.fgHex = "", ""
		case n == 38:
			// Extended colour, whose arguments follow in the same list.
			consumed, hex := extendedColour(fields[i+1:])
			if hex != "" {
				s.fgClass, s.fgHex = "", hex
			}
			i += consumed
		case n == 48:
			// A background this drops, but whose arguments must still be
			// stepped over or they would be read as further attributes.
			consumed, _ := extendedColour(fields[i+1:])
			i += consumed
		default:
			if name, ok := basicColours[n]; ok {
				s.fgClass, s.fgHex = "ansi-"+name, ""
			}
		}
	}
}

// extendedColour reads a 5;N (256-colour) or 2;R;G;B (truecolor) argument list
// and reports how many fields it consumed alongside the resulting hex.
func extendedColour(rest []string) (consumed int, hex string) {
	if len(rest) == 0 {
		return 0, ""
	}
	switch rest[0] {
	case "5":
		if len(rest) < 2 {
			return 1, ""
		}
		n, err := strconv.Atoi(rest[1])
		if err != nil {
			return 2, ""
		}
		return 2, xterm256(n)
	case "2":
		if len(rest) < 4 {
			return len(rest), ""
		}
		var rgb [3]int
		for i := 0; i < 3; i++ {
			v, err := strconv.Atoi(rest[i+1])
			if err != nil || v < 0 || v > 255 {
				return 4, ""
			}
			rgb[i] = v
		}
		return 4, fmt.Sprintf("#%02x%02x%02x", rgb[0], rgb[1], rgb[2])
	}
	return 1, ""
}

// xterm256 converts an xterm palette index to a hex colour: 16 basic colours,
// a 6×6×6 cube, then a 24-step grey ramp.
func xterm256(n int) string {
	switch {
	case n < 0 || n > 255:
		return ""
	case n < 16:
		// The basic range is left to the themeable classes, which is what the
		// caller uses when it can.
		basic := []string{
			"#000000", "#cd3131", "#0dbc79", "#e5e510", "#2472c8", "#bc3fbc", "#11a8cd", "#e5e5e5",
			"#666666", "#f14c4c", "#23d18b", "#f5f543", "#3b8eea", "#d670d6", "#29b8db", "#ffffff",
		}
		return basic[n]
	case n < 232:
		n -= 16
		steps := []int{0, 95, 135, 175, 215, 255}
		return fmt.Sprintf("#%02x%02x%02x", steps[n/36], steps[(n/6)%6], steps[n%6])
	default:
		v := 8 + (n-232)*10
		return fmt.Sprintf("#%02x%02x%02x", v, v, v)
	}
}

// attr renders the style as the class and inline colour of one span, or "" when
// the text needs no wrapping at all.
func (s sgrStyle) attr() string {
	var classes []string
	if s.fgClass != "" {
		classes = append(classes, s.fgClass)
	}
	if s.bold {
		classes = append(classes, "ansi-bold")
	}
	if s.dim {
		classes = append(classes, "ansi-dim")
	}
	if s.italic {
		classes = append(classes, "ansi-italic")
	}
	if s.under {
		classes = append(classes, "ansi-underline")
	}

	var parts []string
	if len(classes) > 0 {
		parts = append(parts, `class="`+strings.Join(classes, " ")+`"`)
	}
	if s.fgHex != "" {
		parts = append(parts, `style="color:`+s.fgHex+`"`)
	}
	return strings.Join(parts, " ")
}
