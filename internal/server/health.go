package server

import (
	"database/sql"
	"fmt"
	"math"
	"net/http"
	"time"

	"quasar/internal/db"
	"quasar/internal/monitor"
	"quasar/internal/vps"
)

// HealthCard is one of the server's three cards: what it is using now, against
// the level its alert fires at, and which way it has moved since yesterday.
type HealthCard struct {
	Key, Label string
	Percent    float64
	Detail     string

	// Threshold is where the alert fires, in percent; zero for an alert that
	// is turned off, which draws no tick.
	Threshold float64

	// Tone is the bar's colour: ok, warn, or err once over the threshold.
	Tone string

	// Delta is the change since the same hour yesterday, worded with its unit
	// — a percentage for the CPU, gigabytes for the memory and the disk, since
	// "1.2" alone could be either. Empty when there is nothing to compare.
	Delta      string
	DeltaUp    bool
	DeltaTitle string
}

// healthTrendWindow is what "now" is averaged over on the cards, and the same
// stretch a day earlier is what it is compared with. An hour rather than the
// latest sample, because a CPU reading a minute apart is noise, and a change
// worked out from noise points whichever way the minute did.
const healthTrendWindow = time.Hour

// diskForecastWindow is how far back the disk's trend is read, and
// diskForecastHorizon how far ahead a forecast is worth saying: a disk that
// will be full in three years is not news.
const (
	diskForecastWindow  = 7 * 24 * time.Hour
	diskForecastHorizon = 365 * 24 * time.Hour
)

func (s *Server) handleSystemPartial(w http.ResponseWriter, r *http.Request) {
	stats, _ := vps.Collect(s.cfg.HostRootPath)
	s.renderPartial(w, "system_stats", healthCards(s.db, stats, time.Now()))
}

// healthCards assembles the three cards from a reading and what the database
// remembers.
func healthCards(database *sql.DB, st vps.Stats, now time.Time) []HealthCard {
	cpuLimit, memLimit, diskLimit := monitor.Thresholds(database)

	nowAvg, okNow := db.ServerAverageBetween(database, now.Add(-healthTrendWindow), now)
	day := now.Add(-24 * time.Hour)
	thenAvg, okThen := db.ServerAverageBetween(database, day.Add(-healthTrendWindow), day)
	compare, compareGB := okNow && okThen, okNow && okThen && nowAvg.HasGB && thenAvg.HasGB

	cpu := HealthCard{Key: "cpu", Label: "CPU", Percent: st.CPUPercent, Threshold: cpuLimit,
		Detail: cpuDetail(st)}
	if compare {
		cpu.setDelta(nowAvg.CPU-thenAvg.CPU, "%", "CPU use")
	}

	mem := HealthCard{Key: "memory", Label: "Memory", Percent: st.MemPercent, Threshold: memLimit,
		Detail: fmt.Sprintf("%.1f / %.1f GB · %s", st.MemUsedGB, st.MemTotalGB, swapDetail(st))}
	if compareGB {
		mem.setDelta(nowAvg.MemGB-thenAvg.MemGB, " GB", "Memory used")
	}

	disk := HealthCard{Key: "disk", Label: "Disk", Percent: st.DiskPercent, Threshold: diskLimit,
		Detail: fmt.Sprintf("%.0f / %.0f GB", st.DiskUsedGB, st.DiskTotalGB)}
	if compareGB {
		disk.setDelta(nowAvg.DiskGB-thenAvg.DiskGB, " GB", "Disk used")
	}
	if pts, err := db.DiskUsedGB(database, now.Add(-diskForecastWindow)); err == nil {
		if left, ok := timeUntilFull(pts, st.DiskTotalGB-st.DiskUsedGB); ok {
			disk.Detail += " · " + fullIn(left)
		}
	}

	cards := []HealthCard{cpu, mem, disk}
	for i := range cards {
		cards[i].Tone = tone(cards[i].Percent, cards[i].Threshold)
	}
	return cards
}

func cpuDetail(st vps.Stats) string {
	cores := "CPU"
	switch {
	case st.CPUs == 1:
		cores = "1 core"
	case st.CPUs > 1:
		cores = fmt.Sprintf("%d cores", st.CPUs)
	}
	return fmt.Sprintf("%s · load %.2f", cores, st.Load1)
}

func swapDetail(st vps.Stats) string {
	if st.SwapTotalGB <= 0 {
		return "no swap"
	}
	return fmt.Sprintf("swap %.1f GB", st.SwapUsedGB)
}

// setDelta words a change, or leaves the card without one when it rounds to
// nothing: an arrow next to 0.0 points somewhere it did not go.
func (c *HealthCard) setDelta(change float64, unit, what string) {
	if math.Abs(change) < 0.05 {
		return
	}
	sign := "+"
	if change < 0 {
		sign = "−"
	}
	c.Delta = fmt.Sprintf("%.1f%s", math.Abs(change), unit)
	c.DeltaUp = change > 0
	c.DeltaTitle = fmt.Sprintf("%s %s%.1f%s since the same hour yesterday", what, sign, math.Abs(change), unit)
}

// tone colours a bar against its alert: red over it, amber in the fifteen
// points under it. With the alert turned off the old fixed levels stand in.
func tone(percent, threshold float64) string {
	warnAt, errAt := 60.0, 85.0
	if threshold > 0 {
		warnAt, errAt = threshold-15, threshold
	}
	switch {
	case percent >= errAt:
		return "err"
	case percent >= warnAt:
		return "warn"
	}
	return "ok"
}

// timeUntilFull fits a straight line through the disk's used gigabytes and
// says when it reaches the free space left, if it is growing and will do so
// inside the horizon.
//
// A day of points at least: a fit over a few hours reads a single image pull as
// a trend and promises a full disk by tonight.
func timeUntilFull(pts []db.MetricPoint, freeGB float64) (time.Duration, bool) {
	if len(pts) < 12 || freeGB <= 0 || pts[len(pts)-1].TS.Sub(pts[0].TS) < 24*time.Hour {
		return 0, false
	}
	// Least squares, with x in days from the first point.
	var sx, sy, sxx, sxy float64
	n := float64(len(pts))
	for _, p := range pts {
		x := p.TS.Sub(pts[0].TS).Hours() / 24
		sx, sy, sxx, sxy = sx+x, sy+p.V1, sxx+x*x, sxy+x*p.V1
	}
	den := n*sxx - sx*sx
	if den == 0 {
		return 0, false
	}
	perDay := (n*sxy - sx*sy) / den
	if perDay <= 0.01 { // flat or shrinking, give or take a log file
		return 0, false
	}
	left := time.Duration(freeGB / perDay * 24 * float64(time.Hour))
	if left > diskForecastHorizon {
		return 0, false
	}
	return left, true
}

func fullIn(d time.Duration) string {
	switch days := int(math.Round(d.Hours() / 24)); {
	case d < 24*time.Hour:
		return "full within a day"
	case days == 1:
		return "full in ~1 day"
	default:
		return fmt.Sprintf("full in ~%d days", days)
	}
}
