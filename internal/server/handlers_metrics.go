package server

import (
	"net/http"
	"time"

	"quasar/internal/chart"
	"quasar/internal/db"
)

// MetricsCard is one card in the History row: a day of one measurement,
// charted, with the figure the window ended on beside its name.
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

const (
	// The window every graph on the dashboard draws, and how finely it is cut.
	// Sixty buckets over a day is one point every twenty-four minutes, which is
	// as much detail as this width of chart can show and no more than the
	// database has to be asked for.
	metricsWindow  = 24 * time.Hour
	metricsBuckets = 60
)

// metricsCard charts one column of the samples.
//
// unit is appended to every figure, and fixedMax pins the top of the scale
// where there is one to pin: a percentage says 100 and means it, where a
// memory figure in megabytes has no ceiling anybody knows in advance and takes
// whatever the day reached.
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
	pts, _ := db.ServerMetrics(s.db, time.Now().Add(-metricsWindow), metricsWindow/metricsBuckets)
	s.renderPartial(w, "metrics", []MetricsCard{
		metricsCard("CPU · 24h", "%", pts, func(p db.MetricPoint) float64 { return p.V1 }, 100),
		metricsCard("Memory · 24h", "%", pts, func(p db.MetricPoint) float64 { return p.V2 }, 100),
	})
}

func (s *Server) handleAppMetricsPartial(w http.ResponseWriter, r *http.Request) {
	a := s.getApp(w, r)
	if a == nil {
		return
	}
	pts, _ := db.AppMetrics(s.db, a.ID, time.Now().Add(-metricsWindow), metricsWindow/metricsBuckets)
	s.renderPartial(w, "metrics", []MetricsCard{
		metricsCard("CPU · 24h", "%", pts, func(p db.MetricPoint) float64 { return p.V1 }, 0),
		metricsCard("Memory · 24h", " MB", pts, func(p db.MetricPoint) float64 { return p.V2 }, 0),
	})
}
