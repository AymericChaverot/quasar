package db

import (
	"testing"
	"time"
)

// The figures beside the percentages are what the server's cards read a change
// in gigabytes from, so they have to come back as they went in.
func TestAServerSampleKeepsItsLoadSwapAndGigabytes(t *testing.T) {
	database := openTestDB(t)
	in := ServerSample{CPU: 12, Mem: 40, Disk: 70, Load1: 0.82, SwapGB: 0.5, MemGB: 6.4, DiskGB: 56}
	if err := RecordServerMetric(database, in); err != nil {
		t.Fatal(err)
	}
	var out ServerSample
	if err := database.QueryRow(`SELECT cpu, mem, disk, load1, swap_gb, mem_gb, disk_gb FROM metrics`).
		Scan(&out.CPU, &out.Mem, &out.Disk, &out.Load1, &out.SwapGB, &out.MemGB, &out.DiskGB); err != nil {
		t.Fatal(err)
	}
	if out != in {
		t.Errorf("stored %+v, read back %+v", in, out)
	}
}

// A bucket keeps its lowest and highest sample beside its average, so a spike
// inside it is still there to draw.
func TestABucketKeepsItsLowestAndHighestSample(t *testing.T) {
	database := openTestDB(t)
	for _, cpu := range []float64{10, 90, 20} {
		if _, err := database.Exec(`INSERT INTO metrics (ts, cpu, mem, disk) VALUES (datetime('now', '-30 seconds'), ?, ?, 50)`,
			cpu, cpu/2); err != nil {
			t.Fatal(err)
		}
	}
	pts, err := ServerMetrics(database, time.Now().Add(-time.Hour), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(pts) != 1 {
		t.Fatalf("got %d buckets, want 1", len(pts))
	}
	p := pts[0]
	if p.V1 != 40 || p.Lo1 != 10 || p.Hi1 != 90 || p.Lo2 != 5 || p.Hi2 != 45 {
		t.Errorf("bucket = %+v, want an average of 40 between 10 and 90, memory 5 to 45", p)
	}
}

// Samples a week old become one row an hour, and a chart reading across the
// fold sees the same averages and extremes it did before it.
func TestFoldedHoursReadLikeTheSamplesTheyReplaced(t *testing.T) {
	database := openTestDB(t)
	hour := time.Now().UTC().Add(-8 * 24 * time.Hour).Truncate(time.Hour)
	at := func(m int) string { return hour.Add(time.Duration(m) * time.Minute).Format("2006-01-02 15:04:05") }
	for i, cpu := range []float64{10, 90, 20} {
		if _, err := database.Exec(`INSERT INTO metrics (ts, cpu, mem, disk, disk_gb) VALUES (?, ?, 40, 70, 56)`, at(i*10), cpu); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(`INSERT INTO app_metrics (app_id, ts, cpu, mem_mb) VALUES ('a', ?, ?, ?)`, at(i*10), cpu, cpu*10); err != nil {
			t.Fatal(err)
		}
	}
	// One fresh sample, which has to stay a sample.
	if err := RecordServerMetric(database, ServerSample{CPU: 5}); err != nil {
		t.Fatal(err)
	}
	before, _ := ServerMetrics(database, hour.Add(-time.Hour), time.Hour)

	if err := FoldMetrics(database, time.Now().Add(-7*24*time.Hour), time.Now().Add(-30*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	var raw, hours, appHours int
	database.QueryRow(`SELECT COUNT(*) FROM metrics`).Scan(&raw)
	database.QueryRow(`SELECT COUNT(*) FROM metrics_hourly`).Scan(&hours)
	database.QueryRow(`SELECT COUNT(*) FROM app_metrics_hourly`).Scan(&appHours)
	if raw != 1 || hours != 1 || appHours != 1 {
		t.Fatalf("after the fold: %d samples, %d server hours, %d app hours; want 1, 1, 1", raw, hours, appHours)
	}

	after, err := ServerMetrics(database, hour.Add(-time.Hour), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) || after[0] != before[0] {
		t.Errorf("the folded hour reads %+v, the samples read %+v", after, before)
	}
	app, _ := AppMetrics(database, "a", hour.Add(-time.Hour), time.Hour)
	if len(app) != 1 || app[0].V1 != 40 || app[0].Lo1 != 10 || app[0].Hi1 != 90 || app[0].Hi2 != 900 {
		t.Errorf("the application's folded hour reads %+v", app)
	}

	// Folding into an hour already folded merges, weighted by samples.
	if _, err := database.Exec(`INSERT INTO metrics (ts, cpu, mem, disk) VALUES (?, 100, 40, 70)`, at(50)); err != nil {
		t.Fatal(err)
	}
	if err := FoldMetrics(database, time.Now().Add(-7*24*time.Hour), time.Now().Add(-30*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	var cpu, hi float64
	var n int
	database.QueryRow(`SELECT cpu, cpu_max, samples FROM metrics_hourly`).Scan(&cpu, &hi, &n)
	if cpu != 55 || hi != 100 || n != 4 {
		t.Errorf("merged hour: cpu %v max %v over %d samples; want 55, 100, 4", cpu, hi, n)
	}

	// And hours past the longer window go.
	if err := FoldMetrics(database, time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	database.QueryRow(`SELECT COUNT(*) FROM metrics_hourly`).Scan(&hours)
	if hours != 0 {
		t.Errorf("%d hours left after dropping everything older than now, want none", hours)
	}
}

// Each application's newest sample, and none for one sampled too long ago.
func TestLatestAppUsageIsEachApplicationsNewestSample(t *testing.T) {
	database := openTestDB(t)
	for _, row := range []struct {
		app, ago string
		cpu      float64
	}{{"a", "-3 minutes", 10}, {"a", "-1 minutes", 20}, {"b", "-2 minutes", 5}, {"stale", "-2 hours", 50}} {
		if _, err := database.Exec(`INSERT INTO app_metrics (app_id, ts, cpu, mem_mb) VALUES (?, datetime('now', ?), ?, ?)`,
			row.app, row.ago, row.cpu, row.cpu*10); err != nil {
			t.Fatal(err)
		}
	}
	got, err := LatestAppUsage(database, time.Now().Add(-5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["a"].CPU != 20 || got["a"].MemMB != 200 || got["b"].CPU != 5 {
		t.Errorf("latest = %+v; want a at 20%% and b at 5%%, nothing for the stale one", got)
	}
}

// One query hands back every application's buckets, apart.
func TestAppUsageSeriesKeepsEachApplicationApart(t *testing.T) {
	database := openTestDB(t)
	for _, row := range []struct {
		app, ago string
		cpu      float64
	}{{"a", "-90 minutes", 10}, {"a", "-20 minutes", 30}, {"b", "-20 minutes", 5}} {
		if _, err := database.Exec(`INSERT INTO app_metrics (app_id, ts, cpu, mem_mb) VALUES (?, datetime('now', ?), ?, 100)`,
			row.app, row.ago, row.cpu); err != nil {
			t.Fatal(err)
		}
	}
	got, err := AppUsageSeries(database, time.Now().Add(-2*time.Hour), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(got["a"]) != 2 || len(got["b"]) != 1 || got["b"][0].V1 != 5 {
		t.Errorf("series = %+v", got)
	}
}
