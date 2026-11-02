package server

import (
	"database/sql"
	"fmt"
	"math"
	"net/http"
	"time"

	"quasar/internal/db"
)

// An application's uptime: a bar a day for the last three months, coloured by
// how many of its health checks answered, and the share that did over the
// usual stretches. Drawn from the checks alone: an application without a
// health path is never asked, and a day nobody asked is shown as such rather
// than as up or down.

// uptimeDays is how many bars the card draws; the retention keeps that many.
const uptimeDays = 90

// UptimeView is the card.
type UptimeView struct {
	AppID string
	// HasCheck is whether the application has a health path, without which
	// no new day will be recorded.
	HasCheck bool
	// Empty is no check on any of the days: nothing to draw.
	Empty   bool
	Bars    []UptimeBar
	Figures []UptimeFigure
}

// UptimeBar is one day.
type UptimeBar struct {
	Tone, Title string
}

// UptimeFigure is the share of checks that answered over a stretch.
type UptimeFigure struct {
	Label, Value, Tone string
}

func (s *Server) handleAppUptimePartial(w http.ResponseWriter, r *http.Request) {
	a := s.getApp(w, r)
	if a == nil {
		return
	}
	s.renderPartial(w, "app_uptime", appUptime(s.db, a.ID, a.HealthPath != "", time.Now()))
}

func appUptime(database *sql.DB, appID string, hasCheck bool, now time.Time) UptimeView {
	v := UptimeView{AppID: appID, HasCheck: hasCheck, Empty: true}
	days, err := db.UptimeDays(database, appID, uptimeDays, now)
	if err != nil {
		return v
	}
	for _, d := range days {
		v.Empty = v.Empty && d.Total == 0
		v.Bars = append(v.Bars, UptimeBar{Tone: uptimeTone(d.Percent()), Title: uptimeTitle(d)})
	}
	for _, f := range []struct {
		label string
		days  int
	}{{"Today", 1}, {"7 days", 7}, {"30 days", 30}, {"90 days", 90}} {
		var ok, total int
		for _, d := range days[len(days)-f.days:] {
			ok, total = ok+d.OK, total+d.Total
		}
		fig := UptimeFigure{Label: f.label, Value: "—", Tone: "none"}
		if total > 0 {
			pct := float64(ok) / float64(total) * 100
			fig.Value, fig.Tone = uptimePercent(pct), uptimeTone(pct)
		}
		v.Figures = append(v.Figures, fig)
	}
	return v
}

// uptimeTone colours a share of answered checks. A check fails now and then on
// a healthy application, a timeout during a deploy, so the green holds to one
// in two hundred; a day past one in twenty is a day something was wrong.
func uptimeTone(pct float64) string {
	switch {
	case pct < 0:
		return "none"
	case pct >= 99.5:
		return "ok"
	case pct >= 95:
		return "warn"
	}
	return "err"
}

// uptimePercent words a share to as many places as it needs: 100% is 100%, and
// a share just under it keeps the decimals that show it is not. Cut rather than
// rounded, so a share never reads better than it was: 98.97% rounded is 99.0%.
func uptimePercent(pct float64) string {
	switch {
	case pct >= 100:
		return "100%"
	case pct >= 99:
		return fmt.Sprintf("%.2f%%", math.Floor(pct*100)/100)
	}
	return fmt.Sprintf("%.1f%%", math.Floor(pct*10)/10)
}

func uptimeTitle(d db.UptimeDay) string {
	date := d.Day.Format("Mon 2 Jan 2006")
	if d.Total == 0 {
		return date + " · no checks"
	}
	failed := d.Total - d.OK
	what := "all checks answered"
	switch {
	case failed == 1:
		what = fmt.Sprintf("1 of %d checks failed", d.Total)
	case failed > 1:
		what = fmt.Sprintf("%d of %d checks failed", failed, d.Total)
	}
	return fmt.Sprintf("%s · %s · %s", date, uptimePercent(d.Percent()), what)
}
