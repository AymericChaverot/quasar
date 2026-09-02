package ui

// A station's theme, as the tokens its own block is drawn from.
//
// themes.css is already built for this: twenty-two custom properties, and the
// components styled once from them. A station theme is those properties
// redefined on the element wrapping the station's tabs — not a second styling
// system, and not a stylesheet a station gets to write.
//
// Three rules hold the whole thing together.
//
// The palette is always inherited. A station sets no bg, no surface, no text
// and no border, so whichever theme the operator chose still decides whether
// the ground is dark or light. That is why a station written on Nebula is
// legible when it lands on Solarized, and why its author never had to think
// about it. Tint is what buys identity back: the accent mixed into the
// surfaces, one number, correct in both directions.
//
// Everything is derived rather than trusted. accent-hover comes from
// color-mix; accent-text is computed here, black or white by real contrast
// against the declared accent, so a station cannot ship an unreadable pair.
//
// And the scope stops at the block. A station is a shareable program, and one
// that could repaint the navigation or the top bar could draw a convincing
// "update available" — or a convincing login screen.

import (
	"fmt"
	"html/template"
	"math"
	"quasar/internal/chart"
	"strconv"
	"strings"
)

// Tokens is the CSS a station's block is drawn with, or nothing at all for a
// document that themed nothing.
//
// It is template.CSS because it is CSS, and it is safe to be: every value in
// it has been through Validate — hex colours matched against a pattern,
// lengths against another, words against a list, and the one free-form value,
// an embedded typeface, against the strict base64 data: URI shape that is the
// only thing a font may be.
func (t Theme) Tokens(scope string) template.CSS {
	var b strings.Builder

	if f := t.FontDisplay; f.Family != "" && f.Src != "" {
		fmt.Fprintf(&b, "@font-face{font-family:%q;src:url(%q);font-display:swap}\n", f.Family, f.Src)
	}

	fmt.Fprintf(&b, "%s{", scope)
	if t.Accent != "" {
		fmt.Fprintf(&b, "--accent:%s;", t.Accent)
		fmt.Fprintf(&b, "--accent-hover:color-mix(in srgb, %s 85%%, %s);", t.Accent, hoverTowards(t.Accent))
		fmt.Fprintf(&b, "--accent-text:%s;", ReadableOn(t.Accent))
	}
	// The first colour is --chart, which every single-series component in
	// Quasar already reads. The rest are numbered from it, which is what a
	// chart of several series cycles through — and a station that named fewer
	// than it draws falls back to --chart rather than to nothing, so a line
	// with no colour of its own is still a line somebody can see.
	if len(t.Chart) > 0 {
		fmt.Fprintf(&b, "--chart:%s;", t.Chart[0])
	}
	for i, c := range t.Chart {
		if i >= chart.MaxColours {
			break
		}
		fmt.Fprintf(&b, "--chart-%d:%s;", i+1, c)
	}

	// The tinted surfaces are separate properties rather than a redefinition
	// of --surface: a custom property cannot be defined in terms of itself,
	// and the mix has to read the value the operator's theme set.
	if tint := t.SurfaceTint(); tint > 0 && t.Accent != "" {
		keep := formatPercent(100 - tint*100)
		fmt.Fprintf(&b, "--station-surface:color-mix(in srgb, var(--surface) %s%%, %s);", keep, t.Accent)
		fmt.Fprintf(&b, "--station-surface-2:color-mix(in srgb, var(--surface-2) %s%%, %s);", keep, t.Accent)
	}

	for _, v := range []struct{ name, value string }{
		{"--radius", string(t.Radius)},
		{"--radius-badge", string(t.RadiusBadge)},
		{"--border-w", string(t.BorderW)},
	} {
		if v.value != "" {
			fmt.Fprintf(&b, "%s:%s;", v.name, v.value)
		}
	}

	if f := t.FontDisplay; f.Family != "" {
		fmt.Fprintf(&b, "--station-display:%q, var(--font-body);", f.Family)
	}
	fmt.Fprintf(&b, "font-family:%s;", bodyFamily(t))
	if scale := t.SizeScale(); scale != 1 {
		fmt.Fprintf(&b, "font-size:calc(1rem * %s);", strconv.FormatFloat(scale, 'f', -1, 64))
	}
	fmt.Fprintf(&b, "--station-case:%s;", caseRule(t.Case))
	fmt.Fprintf(&b, "--station-tracking:%s;", trackingRule(t.Tracking))
	fmt.Fprintf(&b, "--station-pad:%s;", densityRule(t.Density))
	b.WriteString("}")

	return template.CSS(b.String())
}

// bodyFamily is what the station's own text is set in.
func bodyFamily(t Theme) string {
	switch t.FontBody {
	case "display":
		return "var(--station-display, var(--font-body))"
	case "mono":
		return "var(--font-mono)"
	}
	return "var(--font-body)"
}

func caseRule(c string) string {
	if c == "upper" {
		return "uppercase"
	}
	return "none"
}

func trackingRule(tr string) string {
	if tr == "wide" {
		return "0.04em"
	}
	return "normal"
}

func densityRule(d string) string {
	switch d {
	case "compact":
		return "0.625rem"
	case "roomy":
		return "1.375rem"
	}
	return "1rem"
}

// formatPercent writes a percentage without a trailing run of zeroes, because
// the CSS is read by people as often as by browsers.
func formatPercent(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// hoverTowards is which way an accent moves when it is hovered: a dark one
// brightens, a light one darkens. Moving them all the same way makes half the
// accents in the world vanish on hover.
func hoverTowards(hex string) string {
	if luminance(hex) < 0.5 {
		return "white"
	}
	return "black"
}

// ReadableOn is the text colour to put on a background, black or white by real
// contrast rather than by a guess.
//
// It is the care the comment on --accent in Nebula already takes, applied to a
// colour somebody else picked: a station declaring a mid-tone accent and dark
// text on it would be unreadable on its own page, and the author would never
// see it because it looked fine in whichever theme they wrote it in.
func ReadableOn(hex string) string {
	l := luminance(hex)
	onBlack := (l + 0.05) / 0.05
	onWhite := 1.05 / (l + 0.05)
	if onBlack >= onWhite {
		return "#000000"
	}
	return "#ffffff"
}

// luminance is the WCAG relative luminance of a hex colour, 0 for one this
// cannot read — which the validation has already refused, and which would be
// treated as black.
func luminance(hex string) float64 {
	r, g, b, ok := rgb(hex)
	if !ok {
		return 0
	}
	return 0.2126*channel(r) + 0.7152*channel(g) + 0.0722*channel(b)
}

// channel linearises one sRGB channel, by the curve WCAG defines.
func channel(v float64) float64 {
	if v <= 0.04045 {
		return v / 12.92
	}
	return math.Pow((v+0.055)/1.055, 2.4)
}

// rgb splits a validated hex colour into its channels, 0 to 1.
func rgb(hex string) (r, g, b float64, ok bool) {
	h := strings.TrimPrefix(hex, "#")
	switch len(h) {
	case 3, 4:
		h = string([]byte{h[0], h[0], h[1], h[1], h[2], h[2]})
	case 6, 8:
		h = h[:6]
	default:
		return 0, 0, 0, false
	}
	var out [3]float64
	for i := 0; i < 3; i++ {
		v, err := strconv.ParseUint(h[i*2:i*2+2], 16, 8)
		if err != nil {
			return 0, 0, 0, false
		}
		out[i] = float64(v) / 255
	}
	return out[0], out[1], out[2], true
}
