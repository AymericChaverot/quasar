package db

import (
	"database/sql"
	"fmt"
	"testing"
	"time"
)

// Time-based retention alone lets a chatty container fill the disk well inside
// the retention window, so the row cap is the backstop.
func TestPruneLogsKeepsNewestUpToCap(t *testing.T) {
	database := openTestDB(t)
	keyring := testKeyring(t)
	for _, id := range []string{"chatty", "quiet"} {
		if err := InsertApp(database, keyring, &App{
			ID: id, Name: id, Subdomain: id, DeployType: "image", ImageRef: "nginx",
		}); err != nil {
			t.Fatal(err)
		}
	}

	// Rows are inserted directly rather than through AppendLogs so the test
	// can control ts, which CURRENT_TIMESTAMP would collapse to one second.
	const over = 120
	total := MaxLogRowsPerApp + over
	tx, err := database.Begin()
	if err != nil {
		t.Fatal(err)
	}
	stmt, err := tx.Prepare("INSERT INTO app_logs (app_id, ts, line) VALUES (?, datetime('now', ?), ?)")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < total; i++ {
		offset := fmt.Sprintf("-%d seconds", total-i) // oldest first
		if _, err := stmt.Exec("chatty", offset, fmt.Sprintf("line %d", i)); err != nil {
			t.Fatal(err)
		}
	}
	stmt.Close()
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	// An app under the cap must be left completely alone.
	AppendLogs(database, "quiet", []LogEntry{{Line: "only line"}})

	PruneLogs(database)

	if got := countLogs(t, database, "chatty"); got != MaxLogRowsPerApp {
		t.Errorf("chatty app has %d rows, want the cap of %d", got, MaxLogRowsPerApp)
	}
	if got := countLogs(t, database, "quiet"); got != 1 {
		t.Errorf("quiet app has %d rows, want 1 untouched", got)
	}

	// The newest line must survive and the oldest must be the one dropped —
	// truncating the wrong end would leave only stale output.
	var newest, oldest int
	if err := database.QueryRow(
		"SELECT COUNT(*) FROM app_logs WHERE app_id = 'chatty' AND line = ?",
		fmt.Sprintf("line %d", total-1)).Scan(&newest); err != nil {
		t.Fatal(err)
	}
	if newest != 1 {
		t.Error("the newest line was pruned")
	}
	if err := database.QueryRow(
		"SELECT COUNT(*) FROM app_logs WHERE app_id = 'chatty' AND line = 'line 0'").Scan(&oldest); err != nil {
		t.Fatal(err)
	}
	if oldest != 0 {
		t.Error("the oldest line should have been pruned")
	}
}

func TestPruneLogsOnEmptyTable(t *testing.T) {
	PruneLogs(openTestDB(t)) // must not panic or error on a fresh install
}

// Reclaim rewrites the whole database, so it must stay away from one that has
// nothing worth recovering.
func TestReclaimSkipsWhenNothingToRecover(t *testing.T) {
	database := openTestDB(t)
	if freed := Reclaim(database); freed != 0 {
		t.Errorf("freed %d bytes on a fresh database, want 0", freed)
	}
}

func countLogs(t *testing.T, database *sql.DB, appID string) int {
	t.Helper()
	var n int
	if err := database.QueryRow("SELECT COUNT(*) FROM app_logs WHERE app_id = ?", appID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// The samples are written in UTC, by SQLite's own CURRENT_TIMESTAMP, and the
// callers all ask for "the last day" in whatever zone the server is set to.
// The two agree on a machine running UTC, which is what a VPS usually is and
// why this held for as long as it did; east of UTC the window ends before the
// newest sample and the graph is simply empty, with nothing anywhere saying
// so.
func TestAWindowInAnotherZoneStillFindsTheSamples(t *testing.T) {
	database := openTestDB(t)

	if err := RecordAppMetric(database, "app1", 12, 340); err != nil {
		t.Fatal(err)
	}

	// Far enough east that a local clock reads hours ahead of the samples.
	east := time.FixedZone("UTC+9", 9*60*60)
	since := time.Now().In(east).Add(-time.Hour)

	if pts, err := AppMetrics(database, "app1", since, time.Minute); err != nil || len(pts) != 1 {
		t.Errorf("AppMetrics found %d samples in the last hour (%v)", len(pts), err)
	}
	if err := RecordStationSeries(database, "app1", "minecraft", "players", 4); err != nil {
		t.Fatal(err)
	}
	if pts, err := StationSeries(database, "app1", "minecraft", "players", since); err != nil || len(pts) != 1 {
		t.Errorf("StationSeries found %d samples in the last hour (%v)", len(pts), err)
	}

	// And the sweep does not take an hour of samples with it because the clock
	// it was handed reads ahead of them.
	if err := PruneTimeSeries(database, time.Now().In(east).Add(-7*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if pts, _ := AppMetrics(database, "app1", since, time.Minute); len(pts) != 1 {
		t.Error("a fresh sample was pruned by a window expressed in another zone")
	}
}

// The window is bucketed by the query, so the rows the graph is drawn from are
// the buckets and not the samples. Two things have to hold for that to be the
// same graph: each bucket has to be the average of what fell in it, and a
// stretch with no samples has to come back as no bucket rather than as a
// bucket averaging the ones either side of it.
func TestMetricsAreAveragedIntoBucketsWithGapsLeftOpen(t *testing.T) {
	database := openTestDB(t)

	// Two samples inside one ten-minute bucket, a bucket-wide gap, then one
	// more. Written directly so the test owns ts; CURRENT_TIMESTAMP would put
	// all four in the same second.
	base := time.Now().UTC().Truncate(10 * time.Minute).Add(-time.Hour)
	for _, s := range []struct {
		offset time.Duration
		cpu    float64
	}{
		{0, 10},
		{2 * time.Minute, 30},
		{20 * time.Minute, 70},
	} {
		if _, err := database.Exec("INSERT INTO app_metrics (app_id, ts, cpu, mem_mb) VALUES (?, ?, ?, 0)",
			"app1", base.Add(s.offset).Format("2006-01-02 15:04:05"), s.cpu); err != nil {
			t.Fatal(err)
		}
	}

	pts, err := AppMetrics(database, "app1", base.Add(-time.Minute), 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(pts) != 2 {
		t.Fatalf("got %d buckets, want 2 — the empty ten minutes should not be one", len(pts))
	}
	if pts[0].V1 != 20 {
		t.Errorf("first bucket averaged %v, want 20", pts[0].V1)
	}
	if pts[1].V1 != 70 {
		t.Errorf("second bucket averaged %v, want 70", pts[1].V1)
	}
	// The bucket is stamped with its own start rather than with a sample in
	// it, which is what keeps the points evenly spaced.
	if gap := pts[1].TS.Sub(pts[0].TS); gap != 20*time.Minute {
		t.Errorf("buckets are %v apart, want 20m", gap)
	}
}
