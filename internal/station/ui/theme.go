package ui

// A station may change how its own tabs look. What it may change is this
// struct and nothing else, and the shape of the list is the whole argument:
// an accent, a typeface, a shape, a density, an icon.
//
// The palette is never in it. A station sets no bg, surface, text or border,
// so whichever theme the operator chose still decides whether the ground is
// dark or light — which is why a station written on Nebula is legible when it
// lands on Solarized, and why its author never had to think about that. Tint
// is what buys identity back: the accent mixed into the surfaces, one value,
// correct in both directions.
//
// Neither are ok, warn, err and info: a red that means "stopped" everywhere
// else in Quasar must not mean something else inside a station. Nor the log
// colours — rendering logs is Quasar's job.

import (
	"fmt"
	"html/template"
	"regexp"
	"slices"
	"strings"
)

// Theme is the `ui.theme:` block.
type Theme struct {
	// Accent is the one colour a station picks. accent-hover is derived from
	// it with color-mix and accent-text is computed by real contrast, so a
	// station cannot ship an unreadable pair.
	Accent string `yaml:"accent,omitempty"`

	// Tint is how much of the accent is mixed into the surfaces, 0 to 0.2.
	Tint float64 `yaml:"tint,omitempty"`

	// Chart are the series colours, for the components that draw data.
	Chart []string `yaml:"chart,omitempty"`

	// FontDisplay is the typeface for headings, embedded rather than fetched.
	FontDisplay Font `yaml:"font_display,omitempty"`

	// FontBody is which of the three the body text uses.
	FontBody string `yaml:"font_body,omitempty"` // inherit | display | mono

	Scale    float64 `yaml:"scale,omitempty"`    // clamped to [0.9, 1.25]
	Case     string  `yaml:"case,omitempty"`     // normal | upper
	Tracking string  `yaml:"tracking,omitempty"` // normal | wide

	Radius      Length `yaml:"radius,omitempty"`
	RadiusBadge Length `yaml:"radius_badge,omitempty"`
	BorderW     Length `yaml:"border_w,omitempty"`
	Density     string `yaml:"density,omitempty"` // compact | normal | roomy

	// Icon and Banner are data: URIs, for the same reason the fonts are.
	Icon   string `yaml:"icon,omitempty"`
	Banner string `yaml:"banner,omitempty"`
}

// Font is a typeface a station brings with it.
//
// Src takes a data: URI and nothing else. The dashboard ships its typefaces
// inside the binary because it has to render identically on a machine with no
// route to the public internet, which is the normal case for the servers
// Quasar runs on; a station pulling a font from a CDN breaks that, and leaks
// every page view to whoever hosts it.
type Font struct {
	Family string `yaml:"family"`
	Src    string `yaml:"src"`
}

// The vocabularies a theme picks from.
var (
	FontBodies = []string{"inherit", "display", "mono"}
	Cases      = []string{"normal", "upper"}
	Trackings  = []string{"normal", "wide"}
	Densities  = []string{"compact", "normal", "roomy"}
)

// Bounds that are clamped rather than refused. A scale of 3 is a mistake, not
// an attack, and refusing the whole document over it helps nobody; a station
// that asks for one gets 1.25 and stays readable.
const (
	MinScale = 0.9
	MaxScale = 1.25
	MaxTint  = 0.2

	// MaxFontBytes caps an embedded typeface. Measured on the URI as written,
	// which base64 has already inflated by a third — the point is a bound on
	// what the document carries, not on what the font weighs.
	MaxFontBytes = 512 * 1024

	// MaxImageBytes caps an embedded icon or banner. An icon is a mark at the
	// top of a block and a banner is a strip behind it; neither is a
	// photograph, and both travel in every page that draws the station.
	MaxImageBytes = 256 * 1024
)

// hexRe is a CSS hex colour: #rgb, #rgba, #rrggbb or #rrggbbaa.
var hexRe = regexp.MustCompile(`^#([0-9a-fA-F]{3,4}|[0-9a-fA-F]{6}|[0-9a-fA-F]{8})$`)

// lengthRe is a CSS length a station may write: a number, optionally with one
// of the units that make sense for a radius or a border.
var lengthRe = regexp.MustCompile(`^-?[0-9]+(\.[0-9]+)?(px|rem|em|%)?$`)

// dataURIRe is the only shape an embedded font or image may take: a media type
// and base64, and nothing else at all.
//
// It is this strict because the value ends up inside a CSS block. A src
// carrying a quote and a closing tag would otherwise be a way for a station to
// put markup on the page, which is the one thing the whole interface design
// exists to prevent.
var dataURIRe = regexp.MustCompile(`^data:[a-zA-Z0-9!#$&^_.+-]+/[a-zA-Z0-9!#$&^_.+-]+;base64,[A-Za-z0-9+/]+={0,2}$`)

// IconSrc is the icon as a src attribute, marked safe to be one.
//
// html/template writes #ZgotmplZ over a data: URI in a src, which is the right
// default for a value nobody looked at and the wrong answer for the one field
// whose whole content is a data: URI. Without this, every station that brought
// an icon draws a broken image instead of it.
//
// It is safe to mark for the reason Tokens hands back template.CSS: the value
// has been through Validate, which holds it to dataURIRe — an image media type
// and base64, carrying no quote, no space and no angle bracket — and to the
// size cap. A theme that was never validated has an empty icon and reaches
// this with nothing to hand over.
func (t Theme) IconSrc() template.URL { return template.URL(t.Icon) }

// SizeScale is the type scale to render at, clamped.
func (t Theme) SizeScale() float64 {
	if t.Scale == 0 {
		return 1
	}
	return min(max(t.Scale, MinScale), MaxScale)
}

// SurfaceTint is how much accent to mix into the surfaces, clamped.
func (t Theme) SurfaceTint() float64 {
	return min(max(t.Tint, 0), MaxTint)
}

// Validate reports what a theme cannot be given. The clamped values are absent
// on purpose: see the constants above.
func (t Theme) Validate() []error {
	var errs []error
	add := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf("theme: %s", fmt.Sprintf(format, args...)))
	}

	if t.Accent != "" && !hexRe.MatchString(t.Accent) {
		add("accent %q is not a hex colour like #3ba55d", t.Accent)
	}
	for _, c := range t.Chart {
		if !hexRe.MatchString(c) {
			add("chart colour %q is not a hex colour like #8b5cf6", c)
		}
	}

	for _, c := range []struct {
		what, value string
		allowed     []string
	}{
		{"font_body", t.FontBody, FontBodies},
		{"case", t.Case, Cases},
		{"tracking", t.Tracking, Trackings},
		{"density", t.Density, Densities},
	} {
		if c.value != "" && !slices.Contains(c.allowed, c.value) {
			add("%s %q: use one of %s", c.what, c.value, strings.Join(c.allowed, ", "))
		}
	}

	for _, l := range []struct {
		what  string
		value Length
	}{
		{"radius", t.Radius},
		{"radius_badge", t.RadiusBadge},
		{"border_w", t.BorderW},
	} {
		if l.value != "" && !lengthRe.MatchString(string(l.value)) {
			add("%s %q is not a length like 12px", l.what, l.value)
		}
	}

	if f := t.FontDisplay; f.Family != "" || f.Src != "" {
		if f.Family == "" {
			add("font_display carries a src and no family")
		}
		switch {
		case f.Src == "":
			add("font_display %q has no src; a station embeds its typeface rather than naming one", f.Family)
		case !strings.HasPrefix(f.Src, "data:"):
			add("font_display src is fetched from %s; embed the font as a data: URI instead", srcHost(f.Src))
		case len(f.Src) > MaxFontBytes:
			add("font_display is %d KB, over the %d KB a document may carry", len(f.Src)/1024, MaxFontBytes/1024)
		case !dataURIRe.MatchString(f.Src):
			add("font_display src is not a base64 data: URI; it has to be data:font/woff2;base64,…")
		}
	}
	for _, img := range []struct{ what, value string }{{"icon", t.Icon}, {"banner", t.Banner}} {
		switch {
		case img.value == "":
		case !strings.HasPrefix(img.value, "data:"):
			add("%s is fetched from %s; embed it as a data: URI instead", img.what, srcHost(img.value))
		case len(img.value) > MaxImageBytes:
			add("%s is %d KB, over the %d KB a document may carry", img.what, len(img.value)/1024, MaxImageBytes/1024)
		case !dataURIRe.MatchString(img.value):
			add("%s is not a base64 data: URI; it has to be data:image/…;base64,…", img.what)
		}
	}
	return errs
}

// srcHost names where a src would have been fetched from, so the message says
// what the operator's server would have talked to rather than repeating a URL
// that may be a kilometre long.
func srcHost(src string) string {
	s := strings.TrimPrefix(strings.TrimPrefix(src, "https://"), "http://")
	if i := strings.IndexAny(s, "/?#"); i > 0 {
		s = s[:i]
	}
	if s == "" {
		return "elsewhere"
	}
	return s
}
