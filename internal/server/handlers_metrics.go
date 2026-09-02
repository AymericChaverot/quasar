package server

import (
	"net/http"
	"slices"
	"time"

	"quasar/internal/chart"
	"quasar/internal/db"
)

// MetricsCard is one card in the History row: a window of one measurement,
// charted, with the figure it ended on beside its name.
//
// The latest value is carried on the card rather than left to the chart's own
// legend because these are single-series charts, and a legend of one line is a
// line that repeats the heading above it. It is the chart's wording all the
// same — the plot's, not a second opinion — so the number beside the title and
// the number under the pointer are always the same number.
type MetricsCard struct {
	Label  string
	Latest string
	Chart  chart.View
}

// metricsRanges are the windows the picker offers, in the order it offers
// them, and the first is the default. Every one of them has to fit inside
// monitor.Retention — a chart of thirty days over seven days of samples is
// seven days of line and three weeks of empty plot, which reads as an outage
// rather than as a window nobody kept the data for. A test holds the two to
// each other, because the retention window is a long way from here.
var metricsRanges = []string{"24h", "7d"}

// metricsBuckets is how finely a window is cut, whichever window it is. Sixty
// points is as much detail as this width of chart can show, and asking the
// database for one row per point is the difference between reading a day of
// samples and reading sixty of them.
const metricsBuckets = 60

// metricsWindow reads the range a request asked for. Anything it does not
// offer falls back to the default rather than failing: this answers a polled
// partial, and a mistyped query string should redraw the usual chart, not
// leave a hole in the page where one was.
func metricsWindow(r *http.Request) (spec string, window time.Duration) {
	spec = r.URL.Query().Get("range")
	if !slices.Contains(metricsRanges, spec) {
		spec = metricsRanges[0]
	}
	window, err := chart.ParseRange(spec)
	if err != nil { // unreachable while the list above parses; harmless if it stops
		return metricsRanges[0], 24 * time.Hour
	}
	return spec, window
}

// metricsCard charts one column of the samples.
//
// unit is appended to every figure, and fixedMax pins the top of the scale
// where there is one to pin: a percentage says 100 and means it, where a
// memory figure in megabytes has no ceiling anybody knows in advance and takes
// whatever the window reached.
func metricsCard(label, unit string, pts []db.MetricPoint, sel func(db.MetricPoint) float64, fixedMax float64) MetricsCard {
	points := make([]chart.Point, 0, len(pts))
	for _, p := range pts {
		points = append(points, chart.Point{At: p.TS, Value: sel(p)})
	}

	c := MetricsCard{Label: label}
	c.Chart = chart.Build("area", []chart.Series{{Label: label, Points: points}}, unit, fixedMax)
	c.Chart.Label = label
	if len(c.Chart.Plots) == 1 {
		c.Latest = c.Chart.Plots[0].Latest
	}
	return c
}

func (s *Server) handleServerMetricsPartial(w http.ResponseWriter, r *http.Request) {
	spec, window := metricsWindow(r)
	pts, _ := db.ServerMetrics(s.db, time.Now().Add(-window), window/metricsBuckets)
	s.renderPartial(w, "metrics", []MetricsCard{
		metricsCard("CPU · "+spec, "%", pts, func(p db.MetricPoint) float64 { return p.V1 }, 100),
		metricsCard("Memory · "+spec, "%", pts, func(p db.MetricPoint) float64 { return p.V2 }, 100),
		// Sampled beside the other two since the first release and drawn
		// nowhere until now. It is the one of the three that moves slowly and
		// only ever in one direction, which is exactly why a week of it is
		// worth more than a live figure: a disk that will be full on Thursday
		// looks no different today from one that will not.
		metricsCard("Disk · "+spec, "%", pts, func(p db.MetricPoint) float64 { return p.V3 }, 100),
	})
}

func (s *Server) handleAppMetricsPartial(w http.ResponseWriter, r *http.Request) {
	a := s.getApp(w, r)
	if a == nil {
		return
	}
	spec, window := metricsWindow(r)
	pts, _ := db.AppMetrics(s.db, a.ID, time.Now().Add(-window), window/metricsBuckets)
	s.renderPartial(w, "metrics", []MetricsCard{
		metricsCard("CPU · "+spec, "%", pts, func(p db.MetricPoint) float64 { return p.V1 }, 0),
		metricsCard("Memory · "+spec, " MB", pts, func(p db.MetricPoint) float64 { return p.V2 }, 0),
	})
}
