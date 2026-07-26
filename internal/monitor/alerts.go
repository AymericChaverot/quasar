package monitor

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	"quasar/internal/db"
	"quasar/internal/notify"
	"quasar/internal/vps"
)

// Defaults, in percent. Disk is the one that actually kills a single-disk VPS —
// images, build cache, logs and backups all land on it — so it warns earliest.
const (
	AlertDefaultDisk = 85
	AlertDefaultMem  = 90
	AlertDefaultCPU  = 90

	// CPU and memory spike on any deploy or image build, so only a reading
	// that holds across this many samples is worth a notification. Disk usage
	// does not spike, and alerts on the first sample.
	alertSustain = 3

	// Recovery is announced a few points below the threshold, so a value
	// hovering on the line does not alternate messages every minute.
	alertHysteresis = 5
)

// alerter notifies when a host resource crosses its threshold — once per
// crossing rather than once per sample, and again once it recovers.
type alerter struct {
	over   map[string]int  // consecutive samples at or above the threshold
	firing map[string]bool // resources currently in the alerting state
}

func newAlerter() *alerter {
	return &alerter{over: map[string]int{}, firing: map[string]bool{}}
}

// gauge is one host resource evaluated against a configurable threshold.
type gauge struct {
	name     string  // as it appears in the notification
	key      string  // settings key holding the threshold
	fallback float64 // threshold when nothing is configured
	value    float64
	detail   string // human reading included in the message
	sustain  int    // samples above the threshold before alerting
}

func (al *alerter) check(database *sql.DB, s vps.Stats) {
	for _, g := range []gauge{{
		name: "Disk", key: db.SettingAlertDisk, fallback: AlertDefaultDisk, sustain: 1,
		value:  s.DiskPercent,
		detail: fmt.Sprintf("%.0f%% (%.1f of %.1f GB used)", s.DiskPercent, s.DiskUsedGB, s.DiskTotalGB),
	}, {
		name: "Memory", key: db.SettingAlertMem, fallback: AlertDefaultMem, sustain: alertSustain,
		value:  s.MemPercent,
		detail: fmt.Sprintf("%.0f%% (%.1f of %.1f GB used)", s.MemPercent, s.MemUsedGB, s.MemTotalGB),
	}, {
		name: "CPU", key: db.SettingAlertCPU, fallback: AlertDefaultCPU, sustain: alertSustain,
		value:  s.CPUPercent,
		detail: fmt.Sprintf("%.0f%% busy", s.CPUPercent),
	}} {
		al.evaluate(database, g)
	}
}

func (al *alerter) evaluate(database *sql.DB, g gauge) {
	limit := threshold(database, g.key, g.fallback)
	if limit <= 0 { // this resource has alerting turned off
		delete(al.over, g.name)
		delete(al.firing, g.name)
		return
	}

	switch {
	case g.value >= limit:
		al.over[g.name]++
		if !al.firing[g.name] && al.over[g.name] >= g.sustain {
			al.firing[g.name] = true
			notify.Send(database, fmt.Sprintf("Quasar: %s at %s, over the %.0f%% threshold",
				g.name, g.detail, limit))
		}
	case g.value < limit-alertHysteresis:
		al.over[g.name] = 0
		if al.firing[g.name] {
			al.firing[g.name] = false
			notify.Send(database, fmt.Sprintf("Quasar: %s back to %s, under the %.0f%% threshold",
				g.name, g.detail, limit))
		}
	default:
		// In the hysteresis band: no longer climbing, not yet recovered.
		al.over[g.name] = 0
	}
}

// threshold reads a percentage from settings, falling back to the default when
// unset or unparseable. A stored 0 means the operator turned this alert off.
func threshold(database *sql.DB, key string, fallback float64) float64 {
	raw := strings.TrimSpace(db.GetSetting(database, key))
	if raw == "" {
		return fallback
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return fallback
	}
	return v
}
