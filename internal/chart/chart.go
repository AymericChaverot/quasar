package chart

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The chart component: a station's own measurements, drawn.
//
// Server-rendered SVG, the way the dashboard's own sparklines are. That is not
// only consistency: a chart whose points come from Quasar's own tables is
// drawn without a worker ever starting, so a panel refreshing every thirty
// seconds costs a query rather than a process.
//
// The one thing the browser does is follow the pointer, and even that is
// arranging what is written here rather than working anything out — see
// Cursor.
//
// This file is geometry and nothing else — no database, no HTTP, no template.
// What it takes is series of points; what it hands back is the coordinates to
// draw them at, which is what makes it testable without any of the above.

// Kinds are the shapes a chart may take.
var Kinds = []string{"line", "area", "bar", "stacked"}

// MaxColours is how many series colours a theme may name, and therefore how
// many a chart cycles through before it starts again. Eight is more than a
// chart anybody can read holds, and it is the same number as the series a
// station may keep for one application.
const MaxColours = 8

// ParseRange reads how far back a chart draws. Empty is a day, which is the
// window every graph on the dashboard has always been.
//
// Hours and days only. A chart of a station's own measurements is read in
// those units — last night, last week, since the upgrade — and a syntax that
// also took minutes and weeks would be more to learn and no more to say.
func ParseRange(spec string) (time.Duration, error) {
	if spec == "" {
		return 24 * time.Hour, nil
	}
	unit := time.Hour
	switch {
	case strings.HasSuffix(spec, "h"):
	case strings.HasSuffix(spec, "d"):
		unit = 24 * time.Hour
	default:
		return 0, fmt.Errorf("range %q is not a window like 24h or 30d", spec)
	}
	n, err := strconv.Atoi(strings.TrimRight(spec, "hd"))
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("range %q is not a window like 24h or 30d", spec)
	}
	return time.Duration(n) * unit, nil
}

// Point is one sample: when, and how much.
type Point struct {
	At    time.Time
	Value float64
}

// Series is one line, or one set of bars, with the name it goes in the legend
// under.
type Series struct {
	Label  string
	Points []Point
}

// The chart's own geometry, in the units of its viewBox. The left inset is
// room for the value labels and the bottom for the times; a chart with neither
// is a sparkline, and there is already one of those.
const (
	chartW, chartH = 640.0, 180.0
	chartPadL      = 46.0
	chartPadR      = 10.0
	chartPadT      = 10.0
	chartPadB      = 22.0

	// chartGridLines is how many horizontal rules the plot is divided by,
	// counting the axis itself. Three reads as a scale; more reads as graph
	// paper and starts competing with the data for attention.
	chartGridLines = 3
)

// View is a chart ready for a template: everything already positioned,
// so that the template places strings and never computes one.
type View struct {
	Kind string

	// Label is what the chart is announced as to a reader who cannot see it.
	// Set by whoever assembled the series, because only they know whether this
	// one is the server's last day of CPU or the title a station gave a panel.
	Label string

	// W and H are the viewBox, which the page scales to whatever width it has.
	W, H float64

	Plots  []Plot
	Grid   []Grid
	Times  []Time
	Legend bool

	// Cursor is what a hover reads out: every column of the chart, already
	// worded, so that following the pointer is arranging strings rather than
	// deciding what they say.
	Cursor Cursor

	// Empty is a chart with nothing to draw yet, which is what a series looks
	// like for the first few minutes of its life and is not a failure.
	Empty bool
}

// Plot is one series, positioned.
type Plot struct {
	Label string

	// Colour is the CSS custom property to draw it in, cycling through the
	// ones the station's theme declared.
	Colour string

	// Line is the polyline, Area the same closed under itself. Bars is what a
	// bar or stacked chart draws instead.
	Line string
	Area string
	Bars []Bar

	// Latest is the most recent value, for the legend.
	Latest string
}

// Cursor is everything the pointer needs to read a chart back to
// somebody: where the plot is, where each column of it sits, and what each
// series was worth at that column.
//
// It is here, worded and positioned, rather than worked out in the browser,
// because the wording is the same wording the rest of the chart uses — the
// same rounding, the same unit, the same clock — and two implementations of
// that would eventually disagree about what a number is. What the browser does
// is find the nearest column and move three elements to it.
//
// A native <title> was what the sparklines used and what this started as. It
// is the wrong tool at this size: it appears after a second, in the operating
// system's own box, only over the exact pixel of a point, and it can say what
// one series was doing and never what the others were.
type Cursor struct {
	// The plot's own bounds, in viewBox units.
	Left   float64 `json:"left"`
	Right  float64 `json:"right"`
	Top    float64 `json:"top"`
	Bottom float64 `json:"bottom"`

	// At is the moment of each column, worded for reading. X is where that
	// column sits.
	At []string  `json:"at"`
	X  []float64 `json:"x"`

	Series []CursorSeries `json:"series"`
}

// CursorSeries is one series' side of the readout: where its point sits
// in each column and what it read there. A column a series has no point for is
// null in both, which is what a series that started later looks like.
type CursorSeries struct {
	Label  string     `json:"label"`
	Colour string     `json:"colour"`
	Y      []*float64 `json:"y"`
	Value  []string   `json:"value"`
}

// CursorJSON is the readout as the page carries it. An empty object rather
// than an error: a chart whose cursor could not be marshalled is a chart that
// draws and does not follow the pointer, which is worth far more than a panel
// that refuses to render.
func (v View) CursorJSON() string {
	out, err := json.Marshal(v.Cursor)
	if err != nil {
		return "{}"
	}
	return string(out)
}

// Bar is one bar, in viewBox units.
type Bar struct {
	X, Y, W, H float64
}

// Grid is one horizontal rule and the value it stands for.
type Grid struct {
	Y     float64
	Label string
}

// Time is one label under the axis.
type Time struct {
	X     float64
	Label string
	// Anchor keeps the first and last labels inside the plot rather than half
	// off each end of it.
	Anchor string
}

// Build positions a set of series.
//
// unit is appended to every value shown; fixedMax pins the top of the scale
// where the panel declared one, which is what keeps a percentage chart honest
// at 3% instead of redrawing itself as if 3 were a lot.
func Build(kind string, series []Series, unit string, fixedMax float64) View {
	v := View{Kind: kind, W: chartW, H: chartH, Legend: len(series) > 1}

	from, to, ok := span(series)
	if !ok {
		v.Empty = true
		return v
	}

	// A declared max is taken as written — a percentage chart says 100 and
	// means it. One worked out from the data is rounded up to somewhere a
	// person would have put it, because a scale labelled 3.3 and 1.7 reads as
	// an accident and tells nobody anything the data did not already say.
	top := fixedMax
	if top <= 0 {
		top = niceTop(peak(kind, series))
	}

	v.Grid = grid(top, unit)
	v.Times = times(from, to)

	// Bars are drawn from their centres, so the ends of the window need half a
	// bar of room on each side or the first one sits on the value labels and
	// the last one runs out of the plot. One width for the whole chart, from
	// the longest series, so that series drawn together line up.
	bar := barWidth(longest(series), len(series), kind)
	inset := 0.0
	if kind == "bar" || kind == "stacked" {
		inset = bar / 2
		if kind == "bar" && len(series) > 1 {
			inset = bar * float64(len(series)) / 2
		}
	}

	// Where a value sits vertically, and where a moment sits horizontally.
	y := func(value float64) float64 {
		return chartPadT + (chartH-chartPadT-chartPadB)*(1-clamp(value/top))
	}
	x := func(at time.Time) float64 {
		left, right := chartPadL+inset, chartW-chartPadR-inset
		if !to.After(from) {
			return (left + right) / 2
		}
		return left + (right-left)*float64(at.Sub(from))/float64(to.Sub(from))
	}

	// Stacked bars sit on the running total of the series before them, which
	// is what makes them stacked rather than merely drawn over each other.
	stacked := make(map[time.Time]float64)

	// The columns the cursor snaps to: every moment any series has a point
	// for, in order. Series normally share them — they are sampled by the same
	// hook — but one that started later has fewer, and a cursor built from any
	// single series would then be reading the wrong column for the others.
	cols, at := columns(series), momentLabel(from, to)
	v.Cursor = Cursor{
		Left: chartPadL, Right: chartW - chartPadR,
		Top: chartPadT, Bottom: chartH - chartPadB,
	}
	column := make(map[time.Time]int, len(cols))
	for i, when := range cols {
		column[when] = i
		v.Cursor.At = append(v.Cursor.At, at(when))
		v.Cursor.X = append(v.Cursor.X, x(when))
	}

	for i, s := range series {
		p := Plot{Label: s.Label, Colour: chartColour(i)}
		if n := len(s.Points); n > 0 {
			p.Latest = amount(s.Points[n-1].Value, unit)
		}
		read := CursorSeries{Label: s.Label, Colour: p.Colour,
			Y: make([]*float64, len(cols)), Value: make([]string, len(cols))}
		mark := func(pt Point, top float64) {
			j := column[pt.At]
			read.Y[j], read.Value[j] = &top, amount(pt.Value, unit)
		}

		switch kind {
		case "bar", "stacked":
			for _, pt := range s.Points {
				base := stacked[pt.At]
				topY, baseY := y(base+pt.Value), y(base)
				left := x(pt.At) - bar/2
				if kind == "bar" && len(series) > 1 {
					// Side by side, so two series of bars at the same moment
					// are both visible.
					left = x(pt.At) - bar*float64(len(series))/2 + bar*float64(i)
				}
				p.Bars = append(p.Bars, Bar{X: left, Y: topY, W: bar, H: math.Max(baseY-topY, 1)})
				mark(pt, topY)
				if kind == "stacked" {
					stacked[pt.At] = base + pt.Value
				}
			}

		default:
			var line strings.Builder
			for _, pt := range s.Points {
				px, py := x(pt.At), y(pt.Value)
				fmt.Fprintf(&line, "%.1f,%.1f ", px, py)
				mark(pt, py)
			}
			p.Line = strings.TrimSpace(line.String())
			if kind == "area" && len(s.Points) > 0 {
				floor := chartH - chartPadB
				p.Area = fmt.Sprintf("%.1f,%.1f %s %.1f,%.1f",
					x(s.Points[0].At), floor, p.Line, x(s.Points[len(s.Points)-1].At), floor)
			}
		}
		v.Plots = append(v.Plots, p)
		v.Cursor.Series = append(v.Cursor.Series, read)
	}
	return v
}

// columns is every moment any series has a point for, in order.
func columns(series []Series) []time.Time {
	seen := map[time.Time]bool{}
	var out []time.Time
	for _, s := range series {
		for _, p := range s.Points {
			if !seen[p.At] {
				seen[p.At] = true
				out = append(out, p.At)
			}
		}
	}
	slices.SortFunc(out, func(a, b time.Time) int { return a.Compare(b) })
	return out
}

// momentLabel words a moment for the readout, at the precision the window
// makes worth having: a chart of one day is read to the minute, one of a month
// to the hour, and neither wants the other's answer.
func momentLabel(from, to time.Time) func(time.Time) string {
	layout := "15:04"
	switch span := to.Sub(from); {
	case span > 14*24*time.Hour:
		layout = "2 Jan"
	case span > 48*time.Hour:
		layout = "2 Jan 15:04"
	}
	return func(t time.Time) string { return t.Local().Format(layout) }
}

// span is the window the points cover, and whether there are any.
func span(series []Series) (from, to time.Time, ok bool) {
	for _, s := range series {
		for _, p := range s.Points {
			if !ok || p.At.Before(from) {
				from = p.At
			}
			if !ok || p.At.After(to) {
				to = p.At
			}
			ok = true
		}
	}
	return from, to, ok
}

// peak is the largest value the scale has to reach. Stacked bars reach the sum
// of what is stacked at a moment rather than the largest single one.
func peak(kind string, series []Series) float64 {
	if kind != "stacked" {
		var max float64
		for _, s := range series {
			for _, p := range s.Points {
				max = math.Max(max, p.Value)
			}
		}
		return max
	}
	totals := map[time.Time]float64{}
	var max float64
	for _, s := range series {
		for _, p := range s.Points {
			totals[p.At] += p.Value
			max = math.Max(max, totals[p.At])
		}
	}
	return max
}

// niceTop is where a person would have ended the scale: the next round number
// of the right magnitude above the largest value.
//
// The ladder includes 4 as well as the usual 1, 2, 2.5, 5 and 10, because so
// much of what a station measures is a small count of things — four restarts,
// eight players — and putting the top of that scale at 5 wastes a fifth of the
// plot to say the same thing. Every rung halves into another number worth
// reading, which is what the middle rule shows.
func niceTop(max float64) float64 {
	if max <= 0 {
		return 1
	}
	magnitude := math.Pow(10, math.Floor(math.Log10(max)))
	for _, step := range []float64{1, 2, 2.5, 4, 5, 10} {
		if top := step * magnitude; top >= max {
			return top
		}
	}
	return 10 * magnitude
}

// longest is the number of points in the fullest series, which is what the bar
// width has to fit.
func longest(series []Series) int {
	n := 0
	for _, s := range series {
		n = max(n, len(s.Points))
	}
	return n
}

// grid is the horizontal rules, from the axis up.
//
// Only the top one carries the unit. Repeating " online" down the side says
// nothing the first one did not and crowds the plot, and the top label is the
// one somebody reads to learn what the scale is measuring.
func grid(top float64, unit string) []Grid {
	out := make([]Grid, 0, chartGridLines)
	for i := range chartGridLines {
		share := float64(i) / float64(chartGridLines-1)
		label := amount(top*share, "")
		if i == chartGridLines-1 {
			label = amount(top*share, unit)
		}
		out = append(out, Grid{
			Y:     chartPadT + (chartH-chartPadT-chartPadB)*(1-share),
			Label: label,
		})
	}
	return out
}

// times is the labels under the axis: the ends, and the middle when the window
// is wide enough that the ends alone say little.
func times(from, to time.Time) []Time {
	label := func(t time.Time) string {
		if to.Sub(from) > 48*time.Hour {
			return t.Local().Format("2 Jan")
		}
		return t.Local().Format("15:04")
	}
	mid := chartPadL + (chartW-chartPadL-chartPadR)/2
	return []Time{
		{X: chartPadL, Label: label(from), Anchor: "start"},
		{X: mid, Label: label(from.Add(to.Sub(from) / 2)), Anchor: "middle"},
		{X: chartW - chartPadR, Label: label(to), Anchor: "end"},
	}
}

// barWidth keeps bars apart at any density: wide enough to read when there are
// a dozen, thin enough not to overlap when there are three hundred.
func barWidth(points, seriesCount int, kind string) float64 {
	if points < 1 {
		return 1
	}
	slot := (chartW - chartPadL - chartPadR) / float64(points)
	if kind == "bar" && seriesCount > 1 {
		slot /= float64(seriesCount)
	}
	return math.Max(math.Min(slot*0.7, 24), 1)
}

// chartColour cycles through the station's declared chart colours. The theme
// defines as many as it named; past that the numbering wraps, because a chart
// of nine series is a chart nobody can read anyway and running out of colours
// is not the thing to fix about it.
func chartColour(i int) string {
	return fmt.Sprintf("var(--chart-%d, var(--chart))", i%MaxColours+1)
}

// amount is a value as somebody reads it: whole numbers stay whole, because a
// chart of players online should not say 4.0.
func amount(v float64, unit string) string {
	if v == math.Trunc(v) && math.Abs(v) < 1e6 {
		return fmt.Sprintf("%.0f%s", v, unit)
	}
	return fmt.Sprintf("%.1f%s", v, unit)
}

func clamp(share float64) float64 {
	return math.Max(0, math.Min(1, share))
}
