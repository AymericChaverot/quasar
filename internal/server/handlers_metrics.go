package server

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"quasar/internal/db"
)

// Spark is a server-rendered SVG sparkline: 24h of samples, downsampled,
// with native <title> tooltips as the hover layer (no client JS).
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
	sparkBuckets   = 60
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

// downsample averages points into at most n buckets to keep the SVG small.
func downsample(pts []db.MetricPoint, n int) []db.MetricPoint {
	if len(pts) <= n {
		return pts
	}
	out := make([]db.MetricPoint, 0, n)
	size := float64(len(pts)) / float64(n)
	for i := 0; i < n; i++ {
		lo, hi := int(float64(i)*size), int(float64(i+1)*size)
		if hi > len(pts) {
			hi = len(pts)
		}
		if lo >= hi {
			continue
		}
		var agg db.MetricPoint
		for _, p := range pts[lo:hi] {
			agg.V1 += p.V1
			agg.V2 += p.V2
		}
		count := float64(hi - lo)
		agg.V1 /= count
		agg.V2 /= count
		agg.TS = pts[(lo+hi)/2].TS
		out = append(out, agg)
	}
	return out
}

func (s *Server) handleServerMetricsPartial(w http.ResponseWriter, r *http.Request) {
	pts, _ := db.ServerMetrics(s.db, time.Now().Add(-24*time.Hour))
	pts = downsample(pts, sparkBuckets)
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
	pts, _ := db.AppMetrics(s.db, a.ID, time.Now().Add(-24*time.Hour))
	pts = downsample(pts, sparkBuckets)
	s.renderPartial(w, "sparks", []Spark{
		buildSpark("CPU · 24h", "%", pts, func(p db.MetricPoint) float64 { return p.V1 }, 0),
		buildSpark("Memory · 24h", " MB", pts, func(p db.MetricPoint) float64 { return p.V2 }, 0),
	})
}
