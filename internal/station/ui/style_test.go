package ui

import (
	"strings"
	"testing"
)

// A station cannot ship an unreadable pair. The author picked one colour; the
// text that goes on it is worked out here, by real contrast, so it is right in
// every theme rather than in whichever one they wrote it in.
func TestTheTextOnAnAccentIsReadable(t *testing.T) {
	// Every accent the shipped themes use, plus the ends and the middle, which
	// is where a naive rule goes wrong.
	for _, c := range []struct{ accent, want string }{
		{"#d4e815", "#000000"}, // Nebula's sulfur: bright, so dark text
		{"#5b8c3a", "#000000"}, // the documented Minecraft green: mid, and black still wins
		{"#000000", "#ffffff"},
		{"#ffffff", "#000000"},
		{"#808080", "#000000"}, // mid grey too: white text on it is only 4.0:1
		{"#fff", "#000000"},    // and the short form is the same colour
	} {
		if got := ReadableOn(c.accent); got != c.want {
			t.Errorf("ReadableOn(%s) = %s, want %s", c.accent, got, c.want)
		}
	}
}

// Whichever it picks, the pair actually clears the bar. This is the property
// the function exists for, tested as the property rather than as a list.
func TestTheChosenPairClearsTheContrastBar(t *testing.T) {
	for _, accent := range []string{
		"#d4e815", "#5b8c3a", "#38bdf8", "#f87171", "#22d3ee", "#fbbf24",
		"#0f1013", "#fdf6e3", "#8b5cf6", "#767676",
	} {
		l := luminance(accent)
		var ratio float64
		if ReadableOn(accent) == "#000000" {
			ratio = (l + 0.05) / 0.05
		} else {
			ratio = 1.05 / (l + 0.05)
		}
		// 4.5:1 is what WCAG asks of body text. A pair below it would be one
		// somebody has to squint at on their own station's page.
		if ratio < 4.5 {
			t.Errorf("%s: the readable foreground only reaches %.1f:1", accent, ratio)
		}
	}
}

// A scale outside the range is a mistake, not an attack. Refusing the whole
// document over it would help nobody; the station gets the nearest usable
// value and stays readable.
func TestScaleAndTintAreClampedRatherThanRefused(t *testing.T) {
	for _, c := range []struct {
		scale float64
		want  float64
	}{
		{0, 1}, {1.05, 1.05}, {3, MaxScale}, {0.2, MinScale}, {-1, MinScale},
	} {
		if got := (Theme{Scale: c.scale}).SizeScale(); got != c.want {
			t.Errorf("SizeScale(%v) = %v, want %v", c.scale, got, c.want)
		}
	}
	if got := (Theme{Tint: 0.9}).SurfaceTint(); got != MaxTint {
		t.Errorf("SurfaceTint(0.9) = %v, want it clamped to %v", got, MaxTint)
	}
	// And neither is a validation error.
	if errs := (Theme{Scale: 3, Tint: 0.9}).Validate(); len(errs) > 0 {
		t.Errorf("a scale and a tint out of range were refused: %v", errs)
	}
}

// The palette is inherited, always. A station that could set the ground would
// be a station whose author had to think about every theme it might land on —
// and one that could get it wrong.
func TestAStationCannotSetThePalette(t *testing.T) {
	css := string(Theme{Accent: "#5b8c3a", Tint: 0.05}.Tokens("#station"))

	for _, forbidden := range []string{"--bg:", "--text:", "--border:", "--ok:", "--warn:", "--err:", "--log-bg:"} {
		if strings.Contains(css, forbidden) {
			t.Errorf("a station redefined %s", forbidden)
		}
	}
	// --surface itself is never redefined either: a custom property defined in
	// terms of itself is invalid, and the tint has to read what the operator's
	// theme set.
	if strings.Contains(css, "--surface:") {
		t.Error("the tint redefines --surface, which cannot resolve")
	}
	if !strings.Contains(css, "--station-surface:color-mix") {
		t.Errorf("the tint did not reach the surfaces:\n%s", css)
	}
	// And everything it does set lands on the block and nowhere else.
	if !strings.HasPrefix(css, "#station{") {
		t.Errorf("the tokens are not scoped to the block:\n%s", css)
	}
}

func TestTokensCarryWhatTheDocumentDeclared(t *testing.T) {
	css := string(Theme{
		Accent: "#5b8c3a", Radius: "2px", RadiusBadge: "0", BorderW: "2px",
		Case: "upper", Tracking: "wide", Density: "compact", Scale: 1.05,
		Chart:       []string{"#8b5cf6", "#22d3ee"},
		FontDisplay: Font{Family: "Minecraftia", Src: "data:font/woff2;base64,AAAA"},
		FontBody:    "display",
	}.Tokens("#station"))

	for _, want := range []string{
		"--accent:#5b8c3a",
		"--accent-hover:color-mix",
		"--accent-text:#000000",
		"--radius:2px", "--radius-badge:0", "--border-w:2px",
		"--chart:#8b5cf6",
		"--station-case:uppercase", "--station-tracking:0.04em", "--station-pad:0.625rem",
		"font-size:calc(1rem * 1.05)",
		`@font-face{font-family:"Minecraftia"`,
		`--station-display:"Minecraftia"`,
	} {
		if !strings.Contains(css, want) {
			t.Errorf("the tokens do not carry %q:\n%s", want, css)
		}
	}
}

// A document that themed nothing gets the operator's page, untouched.
func TestAnUnthemedStationSetsNoColours(t *testing.T) {
	css := string(Theme{}.Tokens("#station"))
	for _, forbidden := range []string{"--accent:", "--chart:", "--radius:", "@font-face"} {
		if strings.Contains(css, forbidden) {
			t.Errorf("an empty theme set %s:\n%s", forbidden, css)
		}
	}
}

// The value ends up inside a CSS block, so the shape of it is the whole
// defence: a src carrying a quote and a closing tag would be a way for a
// station to put markup on the page.
func TestAnEmbeddedFontHasToBeBase64(t *testing.T) {
	for _, src := range []string{
		`data:font/woff2;base64,AAAA");}</style><script>alert(1)</script>`,
		`data:font/woff2,notbase64`,
		`data:font/woff2;base64,AA AA`,
		`https://fonts.example.com/x.woff2`,
	} {
		errs := Theme{FontDisplay: Font{Family: "X", Src: src}}.Validate()
		if len(errs) == 0 {
			t.Errorf("src %q was accepted", src)
		}
	}
	if errs := (Theme{FontDisplay: Font{Family: "X", Src: "data:font/woff2;base64,AAAA=="}}).Validate(); len(errs) > 0 {
		t.Errorf("a well-formed font was refused: %v", errs)
	}
}
