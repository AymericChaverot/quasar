package server

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"quasar/internal/db"
	"quasar/internal/vps"
)

// A bar turns amber in the fifteen points under its alert and red over it; with
// the alert turned off, the old fixed levels stand in.
func TestABarIsColouredAgainstItsAlert(t *testing.T) {
	for _, c := range []struct {
		percent, threshold float64
		want               string
	}{
		{50, 90, "ok"}, {75, 90, "warn"}, {90, 90, "err"},
		{70, 85, "warn"}, {69, 85, "ok"},
		{59, 0, "ok"}, {60, 0, "warn"}, {85, 0, "err"},
	} {
		if got := tone(c.percent, c.threshold); got != c.want {
			t.Errorf("tone(%v, alert %v) = %s, want %s", c.percent, c.threshold, got, c.want)
		}
	}
}

// A change carries its unit, and one that rounds to nothing is not shown.
func TestAChangeIsWordedWithItsUnit(t *testing.T) {
	var c HealthCard
	c.setDelta(-0.24, " GB", "Memory used")
	if c.Delta != "0.2 GB" || c.DeltaUp || !strings.Contains(c.DeltaTitle, "−0.2 GB") {
		t.Errorf("a fall of 0.24 GB reads %+v", c)
	}
	c = HealthCard{}
	c.setDelta(4.06, "%", "CPU use")
	if c.Delta != "4.1%" || !c.DeltaUp {
		t.Errorf("a rise of 4.06%% reads %+v", c)
	}
	c = HealthCard{}
	c.setDelta(0.04, " GB", "Disk used")
	if c.Delta != "" {
		t.Errorf("a change that rounds to nothing is shown as %q", c.Delta)
	}
}

func hourly(start time.Time, gb ...float64) []db.MetricPoint {
	out := make([]db.MetricPoint, len(gb))
	for i, v := range gb {
		out[i] = db.MetricPoint{TS: start.Add(time.Duration(i) * time.Hour), V1: v}
	}
	return out
}

// A disk growing a gigabyte a day with ten left is full in about ten days; one
// that is flat, shrinking, or measured over too short a stretch says nothing.
func TestTheDiskForecastFollowsItsTrend(t *testing.T) {
	start := time.Now().Add(-48 * time.Hour)
	growing := make([]float64, 49)
	for i := range growing {
		growing[i] = 50 + float64(i)/24
	}
	left, ok := timeUntilFull(hourly(start, growing...), 10)
	if !ok || left < 9*24*time.Hour || left > 11*24*time.Hour {
		t.Errorf("a gigabyte a day with ten left: %s (%v), want about ten days", left, ok)
	}
	if got := fullIn(left); got != "full in ~10 days" {
		t.Errorf("worded %q", got)
	}

	flat := make([]float64, 49)
	for i := range flat {
		flat[i] = 50
	}
	if _, ok := timeUntilFull(hourly(start, flat...), 10); ok {
		t.Error("a flat disk was given a date to fill up")
	}
	if _, ok := timeUntilFull(hourly(start, growing[:12]...), 10); ok {
		t.Error("twelve hours of points were read as a trend")
	}
	slow := make([]float64, 49)
	for i := range slow {
		slow[i] = 50 + float64(i)/24*0.02
	}
	if _, ok := timeUntilFull(hourly(start, slow...), 30); ok {
		t.Error("a disk years from full was given a date")
	}
}

// The cards read the thresholds in force and the change since yesterday.
func TestTheCardsReadTheirAlertsAndTheirChange(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "quasar.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.SetSetting(database, db.SettingAlertDisk, "80"); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		ago                string
		cpu, memGB, diskGB float64
	}{{"-30 minutes", 40, 6, 52}, {"-1470 minutes", 30, 6.5, 50}} {
		if _, err := database.Exec(`INSERT INTO metrics (ts, cpu, mem, disk, mem_gb, disk_gb) VALUES (datetime('now', ?), ?, 0, 0, ?, ?)`,
			row.ago, row.cpu, row.memGB, row.diskGB); err != nil {
			t.Fatal(err)
		}
	}

	cards := healthCards(database, vps.Stats{CPUPercent: 41, CPUs: 4, Load1: 0.82, MemPercent: 40,
		MemUsedGB: 6, MemTotalGB: 16, DiskPercent: 82, DiskUsedGB: 52, DiskTotalGB: 64}, time.Now())
	if len(cards) != 3 {
		t.Fatalf("%d cards", len(cards))
	}
	cpu, mem, disk := cards[0], cards[1], cards[2]
	if cpu.Delta != "10.0%" || !cpu.DeltaUp || cpu.Detail != "4 cores · load 0.82" || cpu.Threshold != 90 {
		t.Errorf("cpu card = %+v", cpu)
	}
	if mem.Delta != "0.5 GB" || mem.DeltaUp || !strings.HasSuffix(mem.Detail, "no swap") {
		t.Errorf("memory card = %+v", mem)
	}
	if disk.Delta != "2.0 GB" || disk.Threshold != 80 || disk.Tone != "err" {
		t.Errorf("disk card = %+v", disk)
	}
}
