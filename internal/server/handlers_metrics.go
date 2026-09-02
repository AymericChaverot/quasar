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

	// Wide is the card given a whole row rather than half of one: the last of
	// an odd number of them, which would otherwise sit against an empty column.
	// It is said here rather than worked out in CSS from the card's position,
	// because the chart inside it is drawn to a wider viewBox to match and the
	// two have to be one decision.
	Wide bool
}

// Measurement is one thing the history can draw, as the picker offers it and
// as the chart reads it.
//
// The list of them is what a page hands its picker, so the two cannot disagree
// about what there is to choose from: the server keeps three and an
// application two, and neither page says so anywhere else.
type Measurement struct {
	Key   string // what the picker sends back
	Name  string // what the picker calls it
	Unit  string
	Max   float64 // 0 for a scale worked out from the window
	Value func(db.MetricPoint) float64
}

// ServerMeasurements and AppMeasurements are what each page can draw, in the
// order the picker offers them and the cards are laid out.
var (
	ServerMeasurements = []Measurement{
		{Key: "cpu", Name: "CPU", Unit: "%", Max: 100, Value: func(p db.MetricPoint) float64 { return p.V1 }},
		{Key: "memory", Name: "Memory", Unit: "%", Max: 100, Value: func(p db.MetricPoint) float64 { return p.V2 }},
		// Sampled beside the other two since the first release and drawn
		// nowhere until now. It is the one of the three that moves slowly and
		// only ever in one direction, which is why a week of it is worth more
		// than a live figure: a disk that will be full on Thursday looks no
		// different today from one that will not.
		{Key: "disk", Name: "Disk", Unit: "%", Max: 100, Value: func(p db.MetricPoint) float64 { return p.V3 }},
	}
	AppMeasurements = []Measurement{
		{Key: "cpu", Name: "CPU", Unit: "%", Value: func(p db.MetricPoint) float64 { return p.V1 }},
		{Key: "memory", Name: "Memory", Unit: " MB", Value: func(p db.MetricPoint) float64 { return p.V2 }},
	}
)

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

// chosen is the measurements a request asked to see, in the order the page
// offers them rather than the order the query string happens to list them —
// unticking Memory should not move Disk, it should close the gap.
//
// Nothing asked for means everything. A request with no `show` at all is the
// page's first load, and one where every box has been unticked would otherwise
// be a heading with nothing under it: an empty section reads as something
// broken, where the full row reads as the state somebody can now change.
func chosen(r *http.Request, offered []Measurement) []Measurement {
	want := r.URL.Query()["show"]
	if len(want) == 0 {
		return offered
	}
	out := make([]Measurement, 0, len(offered))
	for _, m := range offered {
		if slices.Contains(want, m.Key) {
			out = append(out, m)
		}
	}
	if len(out) == 0 {
		return offered
	}
	return out
}

// metricsCards draws one card per measurement, and gives the last of an odd
// number of them the whole of its row.
//
// Which card that is falls out of how many were asked for rather than being
// named here: with all three of the server's it is the disk, with the CPU and
// the disk alone it is the disk in the memory's old place and nothing is wide,
// and with one it is that one across the top. The page never has a half-filled
// row and nothing has to be told which measurement is the odd one.
func metricsCards(pts []db.MetricPoint, want []Measurement, spec string) []MetricsCard {
	cards := make([]MetricsCard, 0, len(want))
	for i, m := range want {
		wide := len(want)%2 == 1 && i == len(want)-1
		cards = append(cards, metricsCard(wide, m.Name+" · "+spec, m.Unit, pts, m.Value, m.Max))
	}
	return cards
}

// metricsCard charts one column of the samples.
//
// unit is appended to every figure, and fixedMax pins the top of the scale
// where there is one to pin: a percentage says 100 and means it, where a
// memory figure in megabytes has no ceiling anybody knows in advance and takes
// whatever the window reached.
func metricsCard(wide bool, label, unit string, pts []db.MetricPoint, sel func(db.MetricPoint) float64, fixedMax float64) MetricsCard {
	points := make([]chart.Point, 0, len(pts))
	for _, p := range pts {
		points = append(points, chart.Point{At: p.TS, Value: sel(p)})
	}

	build := chart.Build
	if wide {
		build = chart.BuildWide
	}
	c := MetricsCard{Label: label, Wide: wide}
	c.Chart = build("area", []chart.Series{{Label: label, Points: points}}, unit, fixedMax)
	c.Chart.Label = label
	if len(c.Chart.Plots) == 1 {
		c.Latest = c.Chart.Plots[0].Latest
	}
	return c
}

func (s *Server) handleServerMetricsPartial(w http.ResponseWriter, r *http.Request) {
	spec, window := metricsWindow(r)
	pts, _ := db.ServerMetrics(s.db, time.Now().Add(-window), window/metricsBuckets)
	s.renderPartial(w, "metrics", metricsCards(pts, chosen(r, ServerMeasurements), spec))
}

func (s *Server) handleAppMetricsPartial(w http.ResponseWriter, r *http.Request) {
	a := s.getApp(w, r)
	if a == nil {
		return
	}
	spec, window := metricsWindow(r)
	pts, _ := db.AppMetrics(s.db, a.ID, time.Now().Add(-window), window/metricsBuckets)
	s.renderPartial(w, "metrics", metricsCards(pts, chosen(r, AppMeasurements), spec))
}
