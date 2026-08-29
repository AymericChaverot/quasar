package ui

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The chart component: a station's own measurements, drawn.
//
// Server-rendered SVG with native <title> tooltips and no client JavaScript,
// which is what the dashboard's sparklines already are. That is not only
// consistency: a chart whose points come from Quasar's own tables is drawn
// without a worker ever starting, so a panel refreshing every thirty seconds
// costs a query rather than a process.
//
// This file is geometry and nothing else — no database, no HTTP, no template.
// What it takes is series of points; what it hands back is the coordinates to
// draw them at, which is what makes it testable without any of the above.

// ChartKinds are the shapes a chart may take.
var ChartKinds = []string{"line", "area", "bar", "stacked"}

// MaxChartColours is how many series colours a theme may name, and therefore
// how many a chart cycles through before it starts again. Eight is more than a
// chart anybody can read holds, and it is the same number as the series a
// station may keep for one application.
const MaxChartColours = 8

// SeriesName is what a series may be called, in the document that charts it
// and in the script that records it alike. It lives here, with the rest of the
// document's vocabulary, so that a name refused at import is refused for the
// same reason at the moment a script writes one.
var SeriesName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

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

// ChartView is a chart ready for a template: everything already positioned,
// so that the template places strings and never computes one.
type ChartView struct {
	Kind string

	// W and H are the viewBox, which the page scales to whatever width it has.
	W, H float64

	Plots  []ChartPlot
	Grid   []ChartGrid
	Times  []ChartTime
	Legend bool

	// Empty is a chart with nothing to draw yet, which is what a series looks
	// like for the first few minutes of its life and is not a failure.
	Empty bool
}

// ChartPlot is one series, positioned.
type ChartPlot struct {
	Label string

	// Colour is the CSS custom property to draw it in, cycling through the
	// ones the station's theme declared.
	Colour string

	// Line is the polyline, Area the same closed under itself. Bars is what a
	// bar or stacked chart draws instead.
	Line string
	Area string
	Bars []ChartBar

	// Dots are the hover targets: transparent circles carrying a <title>,
	// which is the whole tooltip layer and costs no script.
	Dots []ChartDot

	// Latest is the most recent value, for the legend.
	Latest string
}

// ChartBar is one bar, in viewBox units.
type ChartBar struct {
	X, Y, W, H float64
	Title      string
}

// ChartDot is one hover target on a line.
type ChartDot struct {
	X, Y  float64
	Title string
}

// ChartGrid is one horizontal rule and the value it stands for.
type ChartGrid struct {
	Y     float64
	Label string
}

// ChartTime is one label under the axis.
type ChartTime struct {
	X     float64
	Label string
	// Anchor keeps the first and last labels inside the plot rather than half
	// off each end of it.
	Anchor string
}

// Chart positions a set of series.
//
// unit is appended to every value shown; fixedMax pins the top of the scale
// where the panel declared one, which is what keeps a percentage chart honest
// at 3% instead of redrawing itself as if 3 were a lot.
func Chart(kind string, series []Series, unit string, fixedMax float64) ChartView {
	v := ChartView{Kind: kind, W: chartW, H: chartH, Legend: len(series) > 1}

	from, to, ok := span(series)
	if !ok {
		v.Empty = true
		return v
	}

	top := fixedMax
	if top <= 0 {
		top = peak(kind, series) * 1.1
	}
	if top <= 0 {
		top = 1
	}

	v.Grid = grid(top, unit)
	v.Times = times(from, to)

	// Where a value sits vertically, and where a moment sits horizontally.
	y := func(value float64) float64 {
		return chartPadT + (chartH-chartPadT-chartPadB)*(1-clamp(value/top))
	}
	x := func(at time.Time) float64 {
		width := chartW - chartPadL - chartPadR
		if !to.After(from) {
			return chartPadL + width/2
		}
		return chartPadL + width*float64(at.Sub(from))/float64(to.Sub(from))
	}

	// Stacked bars sit on the running total of the series before them, which
	// is what makes them stacked rather than merely drawn over each other.
	stacked := make(map[time.Time]float64)

	for i, s := range series {
		p := ChartPlot{Label: s.Label, Colour: chartColour(i)}
		if n := len(s.Points); n > 0 {
			p.Latest = amount(s.Points[n-1].Value, unit)
		}

		switch kind {
		case "bar", "stacked":
			width := barWidth(len(s.Points), len(series), kind)
			for _, pt := range s.Points {
				base := stacked[pt.At]
				topY, baseY := y(base+pt.Value), y(base)
				left := x(pt.At) - width/2
				if kind == "bar" && len(series) > 1 {
					// Side by side, so two series of bars at the same moment
					// are both visible.
					left = x(pt.At) - width*float64(len(series))/2 + width*float64(i)
				}
				p.Bars = append(p.Bars, ChartBar{
					X: left, Y: topY, W: width, H: math.Max(baseY-topY, 1),
					Title: title(pt, s.Label, unit),
				})
				if kind == "stacked" {
					stacked[pt.At] = base + pt.Value
				}
			}

		default:
			var line strings.Builder
			for _, pt := range s.Points {
				px, py := x(pt.At), y(pt.Value)
				fmt.Fprintf(&line, "%.1f,%.1f ", px, py)
				p.Dots = append(p.Dots, ChartDot{X: px, Y: py, Title: title(pt, s.Label, unit)})
			}
			p.Line = strings.TrimSpace(line.String())
			if kind == "area" && len(s.Points) > 0 {
				floor := chartH - chartPadB
				p.Area = fmt.Sprintf("%.1f,%.1f %s %.1f,%.1f",
					x(s.Points[0].At), floor, p.Line, x(s.Points[len(s.Points)-1].At), floor)
			}
		}
		v.Plots = append(v.Plots, p)
	}
	return v
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

// grid is the horizontal rules, from the axis up.
func grid(top float64, unit string) []ChartGrid {
	out := make([]ChartGrid, 0, chartGridLines)
	for i := range chartGridLines {
		share := float64(i) / float64(chartGridLines-1)
		out = append(out, ChartGrid{
			Y:     chartPadT + (chartH-chartPadT-chartPadB)*(1-share),
			Label: amount(top*share, unit),
		})
	}
	return out
}

// times is the labels under the axis: the ends, and the middle when the window
// is wide enough that the ends alone say little.
func times(from, to time.Time) []ChartTime {
	label := func(t time.Time) string {
		if to.Sub(from) > 48*time.Hour {
			return t.Local().Format("2 Jan")
		}
		return t.Local().Format("15:04")
	}
	mid := chartPadL + (chartW-chartPadL-chartPadR)/2
	return []ChartTime{
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
	return fmt.Sprintf("var(--chart-%d, var(--chart))", i%MaxChartColours+1)
}

// title is one hover label: when, and how much.
func title(p Point, label, unit string) string {
	when := p.At.Local().Format("2 Jan 15:04")
	if label == "" {
		return when + " · " + amount(p.Value, unit)
	}
	return when + " · " + label + " " + amount(p.Value, unit)
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
