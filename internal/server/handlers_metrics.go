package server

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"quasar/internal/db"
)

// Spark is a server-rendered SVG sparkline: a day of samples, bucketed by the
// query that read them, with native <title> tooltips as the hover layer (no
// client JS).
type Spark struct {
	Label  string
	Latest string
	Points string // polyline points
	Area   string // closed polygon under the line
	Dots   []SparkDot
	Empty  bool
}

type SparkDot struct {
	X, Y  float64
	Title string
}

const (
	sparkW, sparkH = 240.0, 48.0
	sparkPad       = 2.0

	// The window every graph on the dashboard draws, and how finely it is cut.
	// Sixty buckets over a day is one point every twenty-four minutes, which is
	// as much detail as 240 pixels of sparkline can show and no more than the
	// database has to be asked for.
	metricsWindow  = 24 * time.Hour
	metricsBuckets = 60
)

func buildSpark(label, unit string, pts []db.MetricPoint, sel func(db.MetricPoint) float64, fixedMax float64) Spark {
	s := Spark{Label: label}
	if len(pts) < 2 {
		s.Empty = true
		return s
	}
	max := fixedMax
	if max <= 0 {
		for _, p := range pts {
			if v := sel(p); v > max {
				max = v
			}
		}
		max *= 1.1
		if max <= 0 {
			max = 1
		}
	}

	s.Latest = fmt.Sprintf("%.1f%s", sel(pts[len(pts)-1]), unit)
	step := (sparkW - 2*sparkPad) / float64(len(pts)-1)
	var line strings.Builder
	for i, p := range pts {
		v := sel(p)
		if v > max {
			v = max
		}
		if v < 0 {
			v = 0
		}
		x := sparkPad + float64(i)*step
		y := sparkPad + (sparkH-2*sparkPad)*(1-v/max)
		fmt.Fprintf(&line, "%.1f,%.1f ", x, y)
		s.Dots = append(s.Dots, SparkDot{
			X: x, Y: y,
			Title: p.TS.Local().Format("15:04") + " · " + fmt.Sprintf("%.1f%s", sel(p), unit),
		})
	}
	s.Points = strings.TrimSpace(line.String())
	lastX := sparkPad + float64(len(pts)-1)*step
	s.Area = fmt.Sprintf("%.1f,%.1f %s %.1f,%.1f", sparkPad, sparkH-sparkPad, s.Points, lastX, sparkH-sparkPad)
	return s
}

func (s *Server) handleServerMetricsPartial(w http.ResponseWriter, r *http.Request) {
	pts, _ := db.ServerMetrics(s.db, time.Now().Add(-metricsWindow), metricsWindow/metricsBuckets)
	s.renderPartial(w, "sparks", []Spark{
		buildSpark("CPU · 24h", "%", pts, func(p db.MetricPoint) float64 { return p.V1 }, 100),
		buildSpark("Memory · 24h", "%", pts, func(p db.MetricPoint) float64 { return p.V2 }, 100),
	})
}

func (s *Server) handleAppMetricsPartial(w http.ResponseWriter, r *http.Request) {
	a := s.getApp(w, r)
	if a == nil {
		return
	}
	pts, _ := db.AppMetrics(s.db, a.ID, time.Now().Add(-metricsWindow), metricsWindow/metricsBuckets)
	s.renderPartial(w, "sparks", []Spark{
		buildSpark("CPU · 24h", "%", pts, func(p db.MetricPoint) float64 { return p.V1 }, 0),
		buildSpark("Memory · 24h", " MB", pts, func(p db.MetricPoint) float64 { return p.V2 }, 0),
	})
}
