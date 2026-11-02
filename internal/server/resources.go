package server

import (
	"fmt"
	"math"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"quasar/internal/db"
	"quasar/internal/vps"
)

// What each application uses, in the dashboard's list. Shown one of three ways,
// chosen beside the list's heading and remembered like the chart's toggles:
// two small bars against the machine, two columns with the last day drawn in
// front of the figure, or not at all.

// Resource display modes, in the order the picker offers them; the first is
// the default.
var resourceModes = []string{"bars", "columns", "off"}

const resourcesCookie = "quasar_resources"

// AppsTable is the dashboard's list of applications, and how it shows what
// each uses.
type AppsTable struct {
	Rows      []AppView
	Resources string
}

// AppResources is one application's figures as the list draws them. Shares
// are against the machine, in percent, for the bars; the lines are the last
// day, for the columns.
type AppResources struct {
	CPU, Mem           string
	CPUShare, MemShare float64
	CPULine, MemLine   string
}

// resourceMode reads the display from the request, writing it back when the
// request came from the picker, and falls back to the cookie and then to the
// bars.
func (s *Server) resourceMode(w http.ResponseWriter, r *http.Request) string {
	if m := r.URL.Query().Get("resources"); slices.Contains(resourceModes, m) {
		// Every poll carries the picker's value; only a change is written.
		if c, err := r.Cookie(resourcesCookie); err == nil && c.Value == m {
			return m
		}
		http.SetCookie(w, &http.Cookie{
			Name: resourcesCookie, Value: m, Path: "/",
			MaxAge: 365 * 24 * 3600, HttpOnly: true,
			Secure: s.cfg.CookieSecure, SameSite: http.SameSiteLaxMode,
		})
		return m
	}
	return savedResourceMode(r)
}

// savedResourceMode is the display somebody chose last time, for the page to
// open its picker on.
func savedResourceMode(r *http.Request) string {
	if c, err := r.Cookie(resourcesCookie); err == nil && slices.Contains(resourceModes, c.Value) {
		return c.Value
	}
	return resourceModes[0]
}

// usageFreshness is how recent a sample has to be to say what an application
// is using now. The monitor samples once a minute; past a few of those the
// application has stopped, or the monitor has, and a figure would be a guess.
const usageFreshness = 5 * time.Minute

// sparkWindow and sparkPoints are what the columns draw: a day, in hours.
const (
	sparkWindow = 24 * time.Hour
	sparkPoints = 24
	sparkW      = 48.0
	sparkH      = 14.0
)

// sparkCache keeps the columns' lines for a minute. The list polls every five
// seconds and the lines are a day of hourly averages, which a minute does not
// move; reading them once a minute rather than twelve times is the difference
// between a query per poll and a query per sample.
type sparkCache struct {
	mu    sync.Mutex
	at    time.Time
	lines map[string][]db.MetricPoint
}

func (c *sparkCache) get(s *Server, now time.Time) map[string][]db.MetricPoint {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lines == nil || now.Sub(c.at) > time.Minute {
		lines, err := db.AppUsageSeries(s.db, now.Add(-sparkWindow), sparkWindow/sparkPoints)
		if err != nil {
			return c.lines // the last good answer, or nothing
		}
		c.lines, c.at = lines, now
	}
	return c.lines
}

// fillResources sets each running application's figures for the mode.
func (s *Server) fillResources(rows []AppView, mode string, now time.Time) {
	if mode == "off" || len(rows) == 0 {
		return
	}
	latest, err := db.LatestAppUsage(s.db, now.Add(-usageFreshness))
	if err != nil {
		return
	}
	cpus, memMB := vps.Capacity()
	var lines map[string][]db.MetricPoint
	if mode == "columns" {
		lines = s.sparks.get(s, now)
	}
	for i := range rows {
		u, ok := latest[rows[i].ID]
		if !ok || rows[i].Status.State != "running" {
			continue
		}
		res := appResources(u, cpus, memMB)
		if pts := lines[rows[i].ID]; len(pts) > 1 {
			res.CPULine = sparkline(pts, func(p db.MetricPoint) float64 { return p.V1 })
			res.MemLine = sparkline(pts, func(p db.MetricPoint) float64 { return p.V2 })
		}
		rows[i].Usage = &res
	}
}

// appResources words one sample and sizes its bars against the machine. CPU is
// counted the way Docker counts it, a hundred per core, so the machine's whole
// is a hundred times its CPUs.
func appResources(u db.AppUsage, cpus int, memMB float64) AppResources {
	res := AppResources{CPU: fmt.Sprintf("%.1f%%", u.CPU), Mem: megabytes(u.MemMB)}
	if cpus > 0 {
		res.CPUShare = share(u.CPU, float64(cpus)*100)
	}
	if memMB > 0 {
		res.MemShare = share(u.MemMB, memMB)
	}
	return res
}

func megabytes(mb float64) string {
	if mb >= 1024 {
		return fmt.Sprintf("%.1f GB", mb/1024)
	}
	return fmt.Sprintf("%.0f MB", mb)
}

// share is part of whole in percent, never under a sliver, so an application
// using something shows a bar rather than an empty track.
func share(part, whole float64) float64 {
	pct := math.Min(part/whole*100, 100)
	if part > 0 {
		pct = math.Max(pct, 2)
	}
	return math.Round(pct*10) / 10
}

// sparkline is a day of one measurement as polyline points, scaled to its own
// highest: the line is there to show its shape, and the figure beside it says
// how much.
func sparkline(pts []db.MetricPoint, value func(db.MetricPoint) float64) string {
	top := 0.0
	for _, p := range pts {
		top = math.Max(top, value(p))
	}
	var b strings.Builder
	for i, p := range pts {
		x := float64(i) / float64(len(pts)-1) * sparkW
		y := sparkH - 1
		if top > 0 {
			y = 1 + (sparkH-2)*(1-value(p)/top)
		}
		fmt.Fprintf(&b, "%.1f,%.1f ", x, y)
	}
	return strings.TrimSpace(b.String())
}
