package server

import (
	"net/http"
	"slices"
	"strings"
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

// showMarker is the hidden field every picker submits. Unchecked boxes send
// nothing at all, so without it a request asking for none of them and a request
// from something that is not the picker are the same request — and the first
// has to draw an empty section where the second has to draw the usual one.
const showMarker = "picker"

// showCookie is where one picker's selection is remembered. Per picker rather
// than per page or per user: the dashboard's history, an application's
// resources and an application's storage are three separate questions, and
// answering one is not answering the others. Kept the way the theme is kept,
// because it is the same kind of thing — a choice about how somebody reads this
// interface, which should still be true tomorrow.
func showCookie(id string) string { return "quasar_show_" + id }

// chosen is the measurements to draw, in the order the page offers them rather
// than the order they were asked for — unticking Memory should not move Disk,
// it should close the gap.
//
// Where the answer comes from, in order: the picker if this request came from
// one, the cookie it wrote last time, and failing both, all of them. Only the
// first can mean none, and none means none — an empty section is what somebody
// asked for when they unticked the last box, and quietly giving them the full
// row back would be the interface disagreeing with its own controls.
func chosen(r *http.Request, id string, offered []Measurement) []Measurement {
	q := r.URL.Query()
	want, asked := q["show"], q.Get(showMarker) != ""
	if !asked {
		c, _ := r.Cookie(showCookie(id))
		if c == nil {
			return offered
		}
		want = strings.Split(c.Value, ",")
	}
	out := make([]Measurement, 0, len(offered))
	for _, m := range offered {
		if slices.Contains(want, m.Key) {
			out = append(out, m)
		}
	}
	return out
}

// remember writes the picker's selection back, so the next page load opens on
// it. Only when the request came from the picker: a plain fetch of the partial
// is not somebody making a choice, and must not overwrite the one they made.
func (s *Server) remember(w http.ResponseWriter, r *http.Request, id string, want []Measurement) {
	if r.URL.Query().Get(showMarker) == "" {
		return
	}
	keys := make([]string, 0, len(want))
	for _, m := range want {
		keys = append(keys, m.Key)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     showCookie(id),
		Value:    strings.Join(keys, ","),
		Path:     "/",
		MaxAge:   365 * 24 * 3600,
		HttpOnly: true,
		Secure:   s.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
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
	want := chosen(r, ServerPicker, ServerMeasurements)
	s.remember(w, r, ServerPicker, want)
	if len(want) == 0 {
		s.renderPartial(w, "metrics", nil)
		return
	}
	pts, _ := db.ServerMetrics(s.db, time.Now().Add(-window), window/metricsBuckets)
	s.renderPartial(w, "metrics", metricsCards(pts, want, spec))
}

func (s *Server) handleAppMetricsPartial(w http.ResponseWriter, r *http.Request) {
	a := s.getApp(w, r)
	if a == nil {
		return
	}
	spec, window := metricsWindow(r)
	want := chosen(r, AppPicker, AppMeasurements)
	s.remember(w, r, AppPicker, want)
	if len(want) == 0 {
		s.renderPartial(w, "metrics", nil)
		return
	}
	pts, _ := db.AppMetrics(s.db, a.ID, time.Now().Add(-window), window/metricsBuckets)
	s.renderPartial(w, "metrics", metricsCards(pts, want, spec))
}

// StorageMeasurements is the one series an application's storage keeps. It is
// a list of one so that the picker and the cards read it the same way
// everything else does; the picker leaves out its Show group when there is only
// one thing in it.
var StorageMeasurements = []Measurement{
	{Key: "size", Name: "Size", Unit: " MB", Value: func(p db.MetricPoint) float64 { return p.V1 }},
}

// handleAppStorageHistoryPartial charts what this application's data directory
// has weighed.
//
// A different window from the resource charts on the same page, and it matters
// which: CPU over a day is a shape, size over a day is a flat line. What this
// answers is which application filled the disk, and that is a question about
// last week — so the samples behind it are kept for a year rather than for
// seven days.
func (s *Server) handleAppStorageHistoryPartial(w http.ResponseWriter, r *http.Request) {
	a := s.getApp(w, r)
	if a == nil {
		return
	}
	spec, window := metricsWindow(r)
	pts, _ := db.AppSizes(s.db, a.ID, time.Now().Add(-window), window/metricsBuckets)
	s.renderPartial(w, "metrics", metricsCards(pts, StorageMeasurements, spec))
}

// The pickers, named. A page and the partial behind it have to agree on which
// selection is being remembered, and this is where they agree.
const (
	ServerPicker  = "server-metrics"
	AppPicker     = "app-metrics"
	StoragePicker = "app-storage"
)

// MeasurementChoice is one entry of a picker as the page draws it: what it
// offers, and whether it is on. Checked comes from the cookie the partial
// wrote, so the boxes open on the selection somebody left rather than on all
// of them.
type MeasurementChoice struct {
	Measurement
	Checked bool
}

// picker is what a page hands its picker template.
func picker(r *http.Request, id string, offered []Measurement) []MeasurementChoice {
	on := chosen(r, id, offered)
	out := make([]MeasurementChoice, 0, len(offered))
	for _, m := range offered {
		out = append(out, MeasurementChoice{Measurement: m, Checked: slices.ContainsFunc(on, func(x Measurement) bool { return x.Key == m.Key })})
	}
	return out
}
