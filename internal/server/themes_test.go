package server

import (
	"os"
	"path/filepath"
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
	css := styleSource(t, "themes.css")
	blocks := map[string]string{}
	for _, m := range themeBlockRe.FindAllStringSubmatch(css, -1) {
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

// The typefaces are embedded rather than fetched from a CDN, which means a
// renamed or forgotten file breaks silently: the browser drops to a system
// face, the page still renders, and no request ever fails. This is the only
// place that would notice. It reads the compiled sheet, which is what the
// browser is actually given.
func TestEveryFontFaceResolvesToAnEmbeddedFile(t *testing.T) {
	css, err := web.Files.ReadFile("static/app.css")
	if err != nil {
		t.Fatal(err)
	}
	urls := regexp.MustCompile(`url\("/static/([^"]+)"\)`).FindAllStringSubmatch(string(css), -1)
	if len(urls) == 0 {
		t.Fatal("app.css declares no @font-face sources; the fonts are no longer embedded")
	}
	for _, m := range urls {
		if _, err := web.Files.ReadFile("static/" + m[1]); err != nil {
			t.Errorf("app.css references /static/%s, which is not in the embedded tree: %v", m[1], err)
		}
	}
}

// The layout preloads the fonts by path, which is a second, independent copy
// of those filenames — one that no stylesheet parse would catch.
func TestPreloadedFontsAreEmbedded(t *testing.T) {
	layout, err := web.Files.ReadFile("templates/layout.html")
	if err != nil {
		t.Fatal(err)
	}
	links := regexp.MustCompile(`rel="preload" href="/static/([^"]+)"`).FindAllStringSubmatch(string(layout), -1)
	if len(links) == 0 {
		t.Fatal("the layout preloads nothing; the fonts will only be found after the stylesheet parses")
	}
	for _, m := range links {
		if _, err := web.Files.ReadFile("static/" + m[1]); err != nil {
			t.Errorf("layout preloads /static/%s, which is not in the embedded tree: %v", m[1], err)
		}
	}
}

// stylesDir holds the stylesheet's sources, which are compiled into
// static/app.css and not embedded themselves.
var stylesDir = filepath.Join("..", "..", "web", "styles")

func styleSource(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(stylesDir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

var styleImportRe = regexp.MustCompile(`(?m)^@import "\./([^"]+)"(.*);$`)

// The stylesheet is a dozen files app.css names one at a time, and a name that
// has gone wrong fails the way a missing font does: quietly. The sheet is left
// out, the page still renders, and one whole domain of the interface comes out
// unstyled. So the two lists are held against each other in both directions —
// an import with nowhere to go, and a sheet nobody imports, are both mistakes.
func TestEverySheetIsImportedAndEveryImportResolves(t *testing.T) {
	imported := map[string]bool{}
	for _, m := range styleImportRe.FindAllStringSubmatch(styleSource(t, "app.css"), -1) {
		imported[m[1]] = true
		if _, err := os.Stat(filepath.Join(stylesDir, m[1])); err != nil {
			t.Errorf("app.css imports %s, which is not in web/styles: %v", m[1], err)
		}
	}
	if len(imported) == 0 {
		t.Fatal("app.css imports no stylesheet at all")
	}

	sheets, err := filepath.Glob(filepath.Join(stylesDir, "*.css"))
	if err != nil {
		t.Fatal(err)
	}
	for _, sheet := range sheets {
		if name := filepath.Base(sheet); name != "app.css" && !imported[name] {
			t.Errorf("web/styles/%s is never imported by app.css, so none of it applies", name)
		}
	}
}

// Every sheet is pulled into the components layer, which is what keeps Tailwind
// utilities above them. One plain @import would outrank every layer at once and
// silently take the utilities out on whatever properties it happens to set.
func TestEveryImportLandsInTheComponentsLayer(t *testing.T) {
	for _, m := range styleImportRe.FindAllStringSubmatch(styleSource(t, "app.css"), -1) {
		if strings.TrimSpace(m[2]) != "layer(components)" {
			t.Errorf("%s is not imported into the components layer, and would outrank every layer: %s", m[1], m[0])
		}
	}
}

// The layout links the compiled sheet, and nothing else: no sheet imported by
// URL, and no stylesheet built in the browser.
func TestTheLayoutLinksTheCompiledSheet(t *testing.T) {
	layout, err := web.Files.ReadFile("templates/layout.html")
	if err != nil {
		t.Fatal(err)
	}
	sheets := regexp.MustCompile(`<link rel="stylesheet" href="([^"]+)">`).FindAllStringSubmatch(string(layout), -1)
	if len(sheets) != 1 || sheets[0][1] != "/static/app.css" {
		t.Errorf("the layout links %v, not just /static/app.css", sheets)
	}
	if strings.Contains(string(layout), "@import") {
		t.Error("the layout still imports a sheet of its own")
	}
	if _, err := web.Files.ReadFile("static/app.css"); err != nil {
		t.Fatal(err)
	}
}

// The compiled sheet carries Tailwind and every source sheet, each in the
// layer the cascade relies on: utilities last, the dashboard's own rules in
// components below them. Run `go generate ./web` if this fails.
func TestTheCompiledSheetKeepsTheLayerOrder(t *testing.T) {
	css, err := web.Files.ReadFile("static/app.css")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(css), "@layer theme, base, components, utilities;") {
		t.Error("app.css does not declare the layer order")
	}
	for _, want := range []string{"@layer utilities {", "@layer components {", `[data-theme="`} {
		if !strings.Contains(string(css), want) {
			t.Errorf("app.css has no %q; was it compiled from web/styles/app.css?", want)
		}
	}
}
