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

	// Every point carries its own hover label, which is the whole tooltip
	// layer and costs no script.
	if n := len(v.Plots[0].Dots); n != 3 {
		t.Errorf("%d hover targets for 3 points", n)
	}
	if title := v.Plots[0].Dots[1].Title; !strings.Contains(title, "players") || !strings.Contains(title, "5") {
		t.Errorf("a hover label reads %q", title)
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
