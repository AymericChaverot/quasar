package ui

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

// at is a moment n minutes into a fixed window, so that a test can say where a
// point should land without depending on when it ran.
func at(n int) time.Time {
	return time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC).Add(time.Duration(n) * time.Minute)
}

func TestAChartPositionsItsPoints(t *testing.T) {
	v := Chart("line", []Series{{Label: "players", Points: []Point{
		{At: at(0), Value: 0},
		{At: at(30), Value: 5},
		{At: at(60), Value: 10},
	}}}, "", 10)

	if v.Empty || len(v.Plots) != 1 {
		t.Fatalf("the chart drew %d plots (empty: %v)", len(v.Plots), v.Empty)
	}
	// The window's ends sit on the plot's ends, and a value at the top of the
	// scale sits at the top of the plot.
	coords := strings.Fields(v.Plots[0].Line)
	if len(coords) != 3 {
		t.Fatalf("the line has %d points: %q", len(coords), v.Plots[0].Line)
	}
	if !strings.HasPrefix(coords[0], "46.0,") {
		t.Errorf("the first point is at %q, want the left edge of the plot", coords[0])
	}
	if !strings.HasPrefix(coords[2], "630.0,") {
		t.Errorf("the last point is at %q, want the right edge of the plot", coords[2])
	}
	if !strings.HasSuffix(coords[2], ",10.0") {
		t.Errorf("a value at the top of the scale is at %q, want the top of the plot", coords[2])
	}

	// And the readout the pointer will use: one column per moment, each
	// carrying where the point sits and what it read, worded here so that the
	// browser never has to decide what a number says.
	if n := len(v.Cursor.X); n != 3 {
		t.Fatalf("%d columns for 3 points", n)
	}
	if got := v.Cursor.Series[0].Value; got[1] != "5" {
		t.Errorf("the middle column reads %q", got[1])
	}
	if y := v.Cursor.Series[0].Y[2]; y == nil || *y != 10 {
		t.Errorf("the last column's mark is at %v, want the top of the plot", y)
	}
	if v.Cursor.Left != 46 || v.Cursor.Right != 630 {
		t.Errorf("the plot's bounds are %v..%v", v.Cursor.Left, v.Cursor.Right)
	}
	// Whole numbers stay whole: a chart of players online should not say 4.0.
	if v.Plots[0].Latest != "10" {
		t.Errorf("the latest value reads %q", v.Plots[0].Latest)
	}
}

// A declared max is what keeps a percentage chart honest. Without one a series
// hovering at 3% redraws itself as if 3 were a lot, which is the reading the
// operator takes away.
func TestADeclaredMaxPinsTheScale(t *testing.T) {
	points := []Point{{At: at(0), Value: 1}, {At: at(60), Value: 3}}

	pinned := Chart("line", []Series{{Points: points}}, "%", 100)
	free := Chart("line", []Series{{Points: points}}, "%", 0)

	if pinned.Grid[len(pinned.Grid)-1].Label != "100%" {
		t.Errorf("the pinned scale tops out at %q", pinned.Grid[len(pinned.Grid)-1].Label)
	}
	if free.Grid[len(free.Grid)-1].Label == "100%" {
		t.Error("a chart with no declared max used one anyway")
	}
	// And a value under a pinned scale is drawn low rather than at the top.
	if y := lastY(pinned.Plots[0].Line); y < 100 {
		t.Errorf("3%% of a pinned 100 was drawn at y=%v, near the top of the plot", y)
	}
}

// Stacked bars are measured against the total at each moment, not against the
// tallest single series, or the stack runs off the top of the plot.
func TestStackedBarsSitOnEachOther(t *testing.T) {
	v := Chart("stacked", []Series{
		{Label: "fabric", Points: []Point{{At: at(0), Value: 6}}},
		{Label: "forge", Points: []Point{{At: at(0), Value: 4}}},
	}, "", 0)

	if len(v.Plots) != 2 || len(v.Plots[0].Bars) != 1 || len(v.Plots[1].Bars) != 1 {
		t.Fatalf("the chart drew %d plots", len(v.Plots))
	}
	first, second := v.Plots[0].Bars[0], v.Plots[1].Bars[0]
	// The second sits on top of the first: it ends where the first begins.
	if got, want := second.Y+second.H, first.Y; got < want-0.5 || got > want+0.5 {
		t.Errorf("the second bar ends at %v and the first starts at %v", got, want)
	}
	if second.X != first.X {
		t.Errorf("stacked bars are side by side: %v against %v", second.X, first.X)
	}
	// Two series, so the legend is worth drawing; one would be noise.
	if !v.Legend {
		t.Error("a chart of two series has no legend")
	}
	if Chart("line", []Series{{Points: []Point{{At: at(0), Value: 1}}}}, "", 0).Legend {
		t.Error("a chart of one series drew a legend")
	}
}

// A series that has just started is empty, and empty is not broken: it is what
// every series looks like for its first few minutes.
func TestAChartWithNoPointsIsEmptyRatherThanBroken(t *testing.T) {
	if v := Chart("line", []Series{{Label: "players"}}, "", 0); !v.Empty {
		t.Error("a series with no points did not come back empty")
	}
	if v := Chart("line", nil, "", 0); !v.Empty {
		t.Error("a chart with no series did not come back empty")
	}
}

func TestParseRange(t *testing.T) {
	for spec, want := range map[string]time.Duration{
		"":     24 * time.Hour,
		"6h":   6 * time.Hour,
		"24h":  24 * time.Hour,
		"30d":  30 * 24 * time.Hour,
		"365d": 365 * 24 * time.Hour,
	} {
		if got, err := ParseRange(spec); err != nil || got != want {
			t.Errorf("ParseRange(%q) = %v, %v; want %v", spec, got, err, want)
		}
	}
	for _, spec := range []string{"week", "0d", "-2h", "10m", "h", "24"} {
		if _, err := ParseRange(spec); err == nil {
			t.Errorf("ParseRange(%q) was accepted", spec)
		}
	}
}

// lastY is the vertical coordinate of a polyline's last point.
func lastY(line string) float64 {
	coords := strings.Fields(line)
	if len(coords) == 0 {
		return 0
	}
	parts := strings.Split(coords[len(coords)-1], ",")
	var y float64
	if len(parts) == 2 {
		y, _ = strconv.ParseFloat(parts[1], 64)
	}
	return y
}

// A scale worked out from the data ends where a person would have ended it. A
// chart labelled 3.3 and 1.7 is arithmetic showing through, and it tells the
// reader nothing the plot did not already say.
func TestAWorkedOutScaleEndsSomewhereRound(t *testing.T) {
	for peak, want := range map[float64]string{
		4:    "4",   // a handful of restarts
		17:   "20",  // players at their busiest
		0.42: "0.5", // a fraction
		230:  "250", // a queue
	} {
		v := Chart("line", []Series{{Points: []Point{{At: at(0), Value: peak}}}}, "", 0)
		if got := v.Grid[len(v.Grid)-1].Label; got != want {
			t.Errorf("a peak of %v put the top of the scale at %q, want %q", peak, got, want)
		}
	}

	// And the unit is on that label alone: repeating it down the side says
	// nothing the first one did not.
	v := Chart("line", []Series{{Points: []Point{{At: at(0), Value: 8}}}}, " online", 0)
	if top := v.Grid[len(v.Grid)-1].Label; !strings.HasSuffix(top, " online") {
		t.Errorf("the top of the scale reads %q, without the unit", top)
	}
	for _, g := range v.Grid[:len(v.Grid)-1] {
		if strings.Contains(g.Label, "online") {
			t.Errorf("the unit is repeated on %q", g.Label)
		}
	}
}

// Bars are drawn from their centres, so the first and the last need half a bar
// of room or one sits on the value labels and the other runs out of the plot —
// which is exactly what they did until somebody looked at a chart rather than
// at its coordinates.
func TestBarsStayInsideThePlot(t *testing.T) {
	for _, kind := range []string{"bar", "stacked"} {
		for _, count := range []int{1, 2, 7, 288} {
			points := make([]Point, count)
			for i := range points {
				points[i] = Point{At: at(i * 5), Value: 3}
			}
			v := Chart(kind, []Series{{Label: "restarts", Points: points}}, "", 0)

			bars := v.Plots[0].Bars
			if len(bars) != count {
				t.Fatalf("%s of %d points drew %d bars", kind, count, len(bars))
			}
			if left := bars[0].X; left < chartPadL-0.01 {
				t.Errorf("%s of %d: the first bar starts at %v, left of the plot at %v",
					kind, count, left, chartPadL)
			}
			if right := bars[len(bars)-1].X + bars[len(bars)-1].W; right > chartW-chartPadR+0.01 {
				t.Errorf("%s of %d: the last bar ends at %v, right of the plot at %v",
					kind, count, right, chartW-chartPadR)
			}
		}
	}
}

// The cursor reads every series at one moment, which is the thing a native
// tooltip could never do: it can say what one line was worth and never what
// the others were beside it.
func TestTheCursorReadsEverySeriesAtOneColumn(t *testing.T) {
	v := Chart("line", []Series{
		{Label: "fabric", Points: []Point{{At: at(0), Value: 2}, {At: at(30), Value: 4}}},
		// Started half an hour late, which is what a series added to a station
		// after the fact looks like.
		{Label: "forge", Points: []Point{{At: at(30), Value: 1}}},
	}, " online", 0)

	// The columns are the union of both, in order — a cursor built from one
	// series would read the wrong column for the other.
	if got := v.Cursor.At; len(got) != 2 {
		t.Fatalf("%d columns for two series sharing one moment: %v", len(got), got)
	}
	if v.Cursor.X[0] >= v.Cursor.X[1] {
		t.Errorf("the columns are not in order: %v", v.Cursor.X)
	}

	fabric, forge := v.Cursor.Series[0], v.Cursor.Series[1]
	if fabric.Value[0] != "2 online" || fabric.Value[1] != "4 online" {
		t.Errorf("fabric reads %v", fabric.Value)
	}
	// The column it has nothing for is null in both, so the browser knows to
	// draw no mark rather than one at the top of the plot.
	if forge.Y[0] != nil || forge.Value[0] != "" {
		t.Errorf("a series with no point at a column reads %v / %q", forge.Y[0], forge.Value[0])
	}
	if forge.Y[1] == nil || forge.Value[1] != "1 online" {
		t.Errorf("forge reads %q at the column it does have", forge.Value[1])
	}
	// Each side of the readout is drawn in its own series' colour, the same
	// one the line is.
	if fabric.Colour == forge.Colour {
		t.Errorf("both series read out in %s", fabric.Colour)
	}
	if fabric.Colour != v.Plots[0].Colour {
		t.Errorf("the readout is %s and the line is %s", fabric.Colour, v.Plots[0].Colour)
	}

	// And it survives the journey to the page.
	if payload := v.CursorJSON(); !strings.Contains(payload, `"4 online"`) || !strings.Contains(payload, `null`) {
		t.Errorf("the payload the page carries is %s", payload)
	}
}

// A stacked chart's mark belongs on top of its own band rather than at its own
// value, or the pointer lands somewhere the eye cannot find.
func TestAStackedCursorFollowsTheBands(t *testing.T) {
	v := Chart("stacked", []Series{
		{Label: "fabric", Points: []Point{{At: at(0), Value: 6}}},
		{Label: "forge", Points: []Point{{At: at(0), Value: 4}}},
	}, "", 0)

	first, second := v.Cursor.Series[0].Y[0], v.Cursor.Series[1].Y[0]
	if first == nil || second == nil {
		t.Fatal("a stacked chart's columns have no marks")
	}
	// Higher up the plot is a smaller y, and the second band sits on the first.
	if *second >= *first {
		t.Errorf("the second band's mark is at %v and the first at %v", *second, *first)
	}
	if *first != v.Plots[0].Bars[0].Y || *second != v.Plots[1].Bars[0].Y {
		t.Error("the marks are not on the tops of the bars they belong to")
	}
}
