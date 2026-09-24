package banner

import (
	"strings"
	"testing"
)

// The version comes out under the name, and no line is wider than the two
// halves of the art side by side.
func TestRender(t *testing.T) {
	out := Render("v1.2.3")
	if !strings.Contains(out, muted+"v1.2.3"+reset) {
		t.Errorf("the version is missing:\n%s", out)
	}
	widest := 0
	for _, l := range lines(logo) {
		widest = max(widest, visible(l))
	}
	name := 0
	for _, l := range lines(word) {
		name = max(name, visible(l))
	}
	for i, l := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if w := visible(l); w > widest+4+name {
			t.Errorf("line %d is %d columns wide", i, w)
		}
	}
	// It ends with its colours reset, so nothing bleeds into what the log
	// prints next.
	if !strings.HasSuffix(strings.TrimRight(out, "\n"), reset) {
		t.Error("the banner does not end reset")
	}
}

func TestRenderWithoutVersion(t *testing.T) {
	if out := Render(""); strings.Contains(out, muted) {
		t.Errorf("an empty version still draws its line:\n%s", out)
	}
}

func TestPlain(t *testing.T) {
	if got := Plain("v1.2.3"); got != "Quasar v1.2.3\n" {
		t.Errorf("Plain = %q", got)
	}
}
