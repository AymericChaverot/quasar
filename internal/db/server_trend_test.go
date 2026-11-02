package db

import (
	"testing"
	"time"
)

// An average leaves out the gigabytes of samples written before they were
// recorded, rather than reading their zero as an empty disk.
func TestServerAverageIgnoresSamplesWithoutGigabytes(t *testing.T) {
	database := openTestDB(t)
	for _, row := range []struct{ cpu, disk float64 }{{10, 0}, {30, 50}, {20, 52}} {
		if _, err := database.Exec(`INSERT INTO metrics (ts, cpu, mem, disk, mem_gb, disk_gb) VALUES (datetime('now', '-10 minutes'), ?, 0, 0, ?, ?)`,
			row.cpu, row.disk/10, row.disk); err != nil {
			t.Fatal(err)
		}
	}
	avg, ok := ServerAverageBetween(database, time.Now().Add(-time.Hour), time.Now())
	if !ok || avg.CPU != 20 || avg.DiskGB != 51 || avg.MemGB != 5.1 || !avg.HasGB {
		t.Errorf("average = %+v (%v); want cpu 20 over all three, disk 51 GB over the two that have it", avg, ok)
	}
	if _, ok := ServerAverageBetween(database, time.Now().Add(-48*time.Hour), time.Now().Add(-47*time.Hour)); ok {
		t.Error("an hour with no samples should have no average")
	}
}

// The disk's trend reads the folded hours and the fresh samples alike.
func TestDiskUsedGBReadsBothHalves(t *testing.T) {
	database := openTestDB(t)
	old := time.Now().UTC().Add(-10 * 24 * time.Hour).Truncate(time.Hour).Format("2006-01-02 15:04:05")
	if _, err := database.Exec(`INSERT INTO metrics_hourly (hour, cpu, cpu_min, cpu_max, mem, mem_min, mem_max, disk, load1, swap_gb, mem_gb, disk_gb, samples)
		VALUES (?, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 40, 60)`, old); err != nil {
		t.Fatal(err)
	}
	if err := RecordServerMetric(database, ServerSample{DiskGB: 45}); err != nil {
		t.Fatal(err)
	}
	pts, err := DiskUsedGB(database, time.Now().Add(-30*24*time.Hour))
	if err != nil || len(pts) != 2 || pts[0].V1 != 40 || pts[1].V1 != 45 {
		t.Errorf("points = %+v (%v); want 40 then 45", pts, err)
	}
}
