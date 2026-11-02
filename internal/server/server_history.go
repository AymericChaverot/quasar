package server

import (
	"database/sql"
	"net/http"
	"slices"
	"strings"
	"time"

	"quasar/internal/chart"
	"quasar/internal/db"
	"quasar/internal/monitor"
)

// The server's history is one chart: CPU, memory and disk on the same scale of
// percent, each of which can be turned off, with what is drawn around them —
// the range each bucket spanned, the levels the alerts fire at, and what
// happened — turned on and off the same way.

// ServerLayers are what the server's chart can draw besides its series.
var ServerLayers = []Measurement{
	{Key: "range", Name: "Min / max"},
	{Key: "alerts", Name: "Alert thresholds"},
	{Key: "events", Name: "Events"},
}

// ServerToggles is everything under the server's chart that can be turned on
// and off, in the order the toggles are drawn.
var ServerToggles = slices.Concat(ServerMeasurements, ServerLayers)

// serverChartHeight is the server's chart's height in the units of its
// viewBox: the one chart in its section, across the whole row, given room to
// show the three measurements apart.
const serverChartHeight = 300

// ServerHistory is the chart as the template draws it, or Nothing when every
// measurement is turned off.
type ServerHistory struct {
	Chart   chart.View
	Nothing bool
}

func (s *Server) handleServerMetricsPartial(w http.ResponseWriter, r *http.Request) {
	spec, window := metricsWindow(r)
	want := chosen(r, ServerPicker, ServerToggles)
	s.remember(w, r, ServerPicker, want)
	s.renderPartial(w, "server_history", serverHistory(s.db, want, spec, window, time.Now()))
}

func serverHistory(database *sql.DB, want []Measurement, spec string, window time.Duration, now time.Time) ServerHistory {
	on := func(key string) bool {
		return slices.ContainsFunc(want, func(m Measurement) bool { return m.Key == key })
	}
	var shown []Measurement
	for _, m := range ServerMeasurements {
		if on(m.Key) {
			shown = append(shown, m)
		}
	}
	if len(shown) == 0 {
		return ServerHistory{Nothing: true}
	}

	pts, _ := db.ServerMetrics(database, now.Add(-window), window/metricsBuckets)
	series := make([]chart.Series, 0, len(shown))
	for _, m := range shown {
		sr := chart.Series{Label: m.Name, Colour: m.Colour, Band: on("range") && m.Range != nil}
		for _, p := range pts {
			pt := chart.Point{At: p.TS, Value: m.Value(p)}
			if sr.Band {
				pt.Lo, pt.Hi = m.Range(p)
			}
			sr.Points = append(sr.Points, pt)
		}
		series = append(series, sr)
	}

	o := chart.Options{FixedMax: 100, Wide: true, Height: serverChartHeight}
	if on("alerts") {
		o.Rules = alertRules(database, shown)
	}
	if on("events") {
		evs, _ := db.ChartEvents(database, now.Add(-window))
		for _, e := range evs {
			o.Events = append(o.Events, chart.Event{At: e.At, Label: e.Label, Kind: e.Kind})
		}
	}
	v := chart.BuildWith("line", series, "%", o)
	v.Label = "Server history · " + spec
	// The toggles under the chart name every series in its colour already.
	v.Legend = false
	return ServerHistory{Chart: v}
}

// alertRules is a rule for each shown measurement's alert, with the ones that
// fire at the same level drawn once and named together.
func alertRules(database *sql.DB, shown []Measurement) []chart.Rule {
	cpu, mem, disk := monitor.Thresholds(database)
	level := map[string]float64{"cpu": cpu, "memory": mem, "disk": disk}
	var (
		order []float64
		names = map[float64][]string{}
	)
	for _, m := range shown {
		v := level[m.Key]
		if v <= 0 {
			continue // that alert is off
		}
		if _, ok := names[v]; !ok {
			order = append(order, v)
		}
		names[v] = append(names[v], m.Name)
	}
	out := make([]chart.Rule, 0, len(order))
	for _, v := range order {
		label := strings.Join(names[v], " / ") + " alert"
		out = append(out, chart.Rule{Value: v, Label: label})
	}
	return out
}
