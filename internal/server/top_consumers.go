package server

import (
	"cmp"
	"database/sql"
	"fmt"
	"net/http"
	"slices"
	"time"

	"quasar/internal/chart"
	"quasar/internal/db"
	"quasar/internal/secrets"
)

// Top consumers: which applications the server's memory or CPU goes to, over
// the window the history is set to, stacked, and as they stand now. Behind a
// button rather than on the page: it answers a question somebody has when a
// line on the chart above looks wrong, and costs a query per application.

// topConsumersShown is how many applications are drawn by name; the rest are
// summed into one band. Past six, colours stop telling series apart.
const topConsumersShown = 6

// consumerColours are the bands' colours, in order: the theme's own, so every
// theme draws them in colours it chose. The rest are summed into a grey band.
var consumerColours = []string{
	"var(--chart)", "var(--chart-memory)", "var(--chart-disk)",
	"var(--info)", "var(--ok)", "var(--warn)",
}

const othersColour = "var(--text-faint)"

// Consumer is one application in the ranking.
type Consumer struct {
	Name, Colour, Value string
	// Share is its bar's length against the largest, in percent.
	Share float64
}

// TopConsumers is the modal's content.
type TopConsumers struct {
	Metric  string // "mem" or "cpu"
	Chart   chart.View
	Ranking []Consumer
	None    bool // no application has been sampled in the window
}

func (s *Server) handleTopConsumersPartial(w http.ResponseWriter, r *http.Request) {
	spec, window := metricsWindow(r)
	metric := r.URL.Query().Get("metric")
	if metric != "cpu" {
		metric = "mem"
	}
	s.renderPartial(w, "top_consumers", topConsumers(s.db, s.keyring, metric, spec, window, time.Now()))
}

type consumerSeries struct {
	name   string
	points []chart.Point
	mean   float64
}

func topConsumers(database *sql.DB, keyring *secrets.Keyring, metric, spec string, window time.Duration, now time.Time) TopConsumers {
	out := TopConsumers{Metric: metric}
	apps, err := db.ListApps(database, keyring)
	if err != nil {
		out.None = true
		return out
	}

	unit, value := "%", func(p db.MetricPoint) float64 { return p.V1 }
	if metric == "mem" {
		unit, value = " GB", func(p db.MetricPoint) float64 { return p.V2 / 1024 }
	}

	var all []consumerSeries
	for _, a := range apps {
		pts, _ := db.AppMetrics(database, a.ID, now.Add(-window), window/metricsBuckets)
		if len(pts) == 0 {
			continue
		}
		cs := consumerSeries{name: a.Name}
		for _, p := range pts {
			v := value(p)
			cs.points = append(cs.points, chart.Point{At: p.TS, Value: v})
			cs.mean += v
		}
		cs.mean /= float64(len(pts))
		all = append(all, cs)
	}
	if len(all) == 0 {
		out.None = true
		return out
	}
	// The heaviest at the bottom of the stack, where its band is flattest and
	// easiest to read.
	slices.SortStableFunc(all, func(a, b consumerSeries) int { return cmp.Compare(b.mean, a.mean) })

	colour := map[string]string{}
	var series []chart.Series
	for i, cs := range all {
		if i < topConsumersShown {
			colour[cs.name] = consumerColours[i]
			series = append(series, chart.Series{Label: cs.name, Colour: consumerColours[i], Points: cs.points})
			continue
		}
		if i == topConsumersShown {
			series = append(series, chart.Series{Label: fmt.Sprintf("%d others", len(all)-topConsumersShown), Colour: othersColour})
		}
		series[topConsumersShown].Points = sumPoints(series[topConsumersShown].Points, cs.points)
	}
	out.Chart = chart.BuildWith(chart.StackedArea, series, unit, chart.Options{})
	out.Chart.Label = "Top consumers · " + spec

	// The ranking is now, not the window: what each is using at this moment.
	latest, _ := db.LatestAppUsage(database, now.Add(-5*time.Minute))
	type row struct {
		name string
		v    float64
	}
	var rows []row
	for _, a := range apps {
		u, ok := latest[a.ID]
		if !ok {
			continue
		}
		v := u.CPU
		if metric == "mem" {
			v = u.MemMB / 1024
		}
		rows = append(rows, row{a.Name, v})
	}
	slices.SortStableFunc(rows, func(a, b row) int { return cmp.Compare(b.v, a.v) })
	for _, rw := range rows {
		c := Consumer{Name: rw.name, Colour: othersColour, Value: fmt.Sprintf("%.1f%s", rw.v, unit)}
		if col, ok := colour[rw.name]; ok {
			c.Colour = col
		}
		if rows[0].v > 0 {
			c.Share = rw.v / rows[0].v * 100
		}
		out.Ranking = append(out.Ranking, c)
	}
	return out
}

// sumPoints adds b into a, moment by moment.
func sumPoints(a, b []chart.Point) []chart.Point {
	at := map[time.Time]int{}
	for i, p := range a {
		at[p.At] = i
	}
	for _, p := range b {
		if i, ok := at[p.At]; ok {
			a[i].Value += p.Value
			continue
		}
		at[p.At] = len(a)
		a = append(a, p)
	}
	slices.SortFunc(a, func(x, y chart.Point) int { return x.At.Compare(y.At) })
	return a
}
