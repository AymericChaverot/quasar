// Package banner draws Quasar's banner: the mark — a ring with a jet off each
// pole — beside the name, over the version. The dashboard prints it as it
// starts, first thing in its logs.
//
// Both halves are ANSI art kept as files. The mark is rendered from its shape
// in 24-bit colour, two pixels to a character with ▀ and ▄, which needs a
// background colour as much as a foreground; the name is drawn by hand. Where
// colour is not wanted (NO_COLOR), Plain gives the same in one line.
package banner

import (
	_ "embed"
	"regexp"
	"strings"
	"unicode/utf8"
)

//go:embed logo.ans
var logo string

//go:embed word.ans
var word string

// sgr matches the colour escapes in the art, which take no room on screen.
var sgr = regexp.MustCompile("\x1b\\[[0-9;]*m")

const (
	reset = "\x1b[0m"
	muted = "\x1b[38;2;150;152;160m"
)

// Render is the banner for a dashboard at version; with no version, the line
// under the name is left out.
func Render(version string) string {
	text := lines(word)
	if version != "" {
		text = append(text, "", muted+version+reset)
	}

	mark := lines(logo)
	width := 0
	for _, l := range mark {
		width = max(width, visible(l))
	}
	top := max(0, (len(mark)-len(text))/2)
	var b strings.Builder
	for i := range max(len(mark), top+len(text)) {
		line := ""
		if i < len(mark) {
			line = mark[i]
		}
		if j := i - top; j >= 0 && j < len(text) {
			line += strings.Repeat(" ", width-visible(line)+4) + text[j]
		}
		b.WriteString(strings.TrimRight(line, " ") + "\n")
	}
	return b.String()
}

// Plain is the banner without the art, for output where escapes are not
// wanted.
func Plain(version string) string {
	if version == "" {
		return "Quasar\n"
	}
	return "Quasar " + version + "\n"
}

func lines(s string) []string {
	return strings.Split(strings.TrimRight(s, "\n"), "\n")
}

// visible is how many columns a line of art takes: its characters, less the
// escapes.
func visible(s string) int {
	return utf8.RuneCountInString(sgr.ReplaceAllString(s, ""))
}
