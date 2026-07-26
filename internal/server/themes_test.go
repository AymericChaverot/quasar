package server

import (
	"regexp"
	"strings"
	"testing"

	"quasar/web"
)

// themeBlockRe matches the variable block of a theme — "[data-theme="x"] { … }"
// — and skips the per-theme component tweaks, whose selectors carry something
// between the attribute and the brace.
var themeBlockRe = regexp.MustCompile(`\[data-theme="([a-z]+)"\]\s*\{([^}]*)\}`)

func themeBlocks(t *testing.T) map[string]string {
	t.Helper()
	css, err := web.Files.ReadFile("static/themes.css")
	if err != nil {
		t.Fatal(err)
	}
	blocks := map[string]string{}
	for _, m := range themeBlockRe.FindAllStringSubmatch(string(css), -1) {
		if _, seen := blocks[m[1]]; !seen {
			blocks[m[1]] = m[2]
		}
	}
	if len(blocks) == 0 {
		t.Fatal("no theme blocks found in themes.css")
	}
	return blocks
}

// A theme offered in Settings but absent from the stylesheet silently renders
// as the default, which looks like the theme switcher being broken.
func TestEveryThemeHasAStylesheetBlock(t *testing.T) {
	blocks := themeBlocks(t)
	for _, theme := range themes {
		if _, ok := blocks[theme.ID]; !ok {
			t.Errorf("theme %q has no [data-theme=%q] block in themes.css", theme.ID, theme.ID)
		}
	}
	for id := range blocks {
		if !validTheme(id) {
			t.Errorf("themes.css defines %q, which is not selectable in Settings", id)
		}
	}
}

// Every component is styled from the variables, so one a theme forgets falls
// back to the default's value — a dark accent on a light theme, and no error
// anywhere to say so.
func TestEveryThemeDefinesEveryVariable(t *testing.T) {
	blocks := themeBlocks(t)
	varRe := regexp.MustCompile(`(--[a-z0-9-]+)\s*:`)

	declared := func(id string) map[string]bool {
		out := map[string]bool{}
		for _, m := range varRe.FindAllStringSubmatch(blocks[id], -1) {
			out[m[1]] = true
		}
		return out
	}

	// The default block is the reference. The fonts are deliberately left out:
	// themes inherit them from :root unless they mean to replace them.
	want := declared(themes[0].ID)
	delete(want, "--font-body")
	delete(want, "--font-mono")
	if len(want) < 10 {
		t.Fatalf("the default theme declares only %d variables; the reference block was not parsed", len(want))
	}

	for _, theme := range themes {
		got := declared(theme.ID)
		var missing []string
		for name := range want {
			if !got[name] {
				missing = append(missing, name)
			}
		}
		if len(missing) > 0 {
			t.Errorf("theme %q does not define %s", theme.ID, strings.Join(missing, ", "))
		}
	}
}
