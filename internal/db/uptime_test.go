package db

import (
	"fmt"
	"testing"
	"time"
)

// Probes a week old are counted into their day and dropped; the day reads the
// same before and after, and adds up when it is folded in two goes.
func TestHealthProbesFoldIntoDays(t *testing.T) {
	database := openTestDB(t)
	insert := func(app, ago string, ok bool) {
		t.Helper()
		if _, err := database.Exec(`INSERT INTO health_history (app_id, ts, ok) VALUES (?, datetime('now', ?), ?)`,
			app, ago, ok); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	// Ten days ago at noon: nine probes up, one down.
	tenDays := now.UTC().Truncate(24*time.Hour).AddDate(0, 0, -10).Add(12 * time.Hour)
	for i := range 10 {
		at := tenDays.Add(time.Duration(i) * time.Minute)
		ago := fmt.Sprintf("-%d seconds", int(now.Sub(at).Seconds()))
		insert("web", ago, i != 0)
	}
	// Today: two probes, one up.
	insert("web", "-1 seconds", true)
	insert("web", "-2 seconds", false)
	// Another application's probes are another application's.
	insert("other", "-3 seconds", false)

	before, err := UptimeDays(database, "web", 30, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 30 {
		t.Fatalf("%d days, want 30", len(before))
	}
	if d := before[19]; d.OK != 9 || d.Total != 10 || d.Percent() != 90 {
		t.Errorf("ten days ago = %+v", d)
	}
	if d := before[29]; d.OK != 1 || d.Total != 2 || !d.Day.Equal(now.UTC().Truncate(24*time.Hour)) {
		t.Errorf("today = %+v", d)
	}
	if d := before[0]; d.Total != 0 || d.Percent() != -1 {
		t.Errorf("a day without probes = %+v, %v", d, d.Percent())
	}

	if err := FoldHealth(database, now.Add(-7*24*time.Hour), now.Add(-90*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	var raw int
	if err := database.QueryRow(`SELECT COUNT(*) FROM health_history WHERE app_id = 'web'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw != 2 {
		t.Errorf("%d probes left after folding, want today's 2", raw)
	}
	after, err := UptimeDays(database, "web", 30, now)
	if err != nil {
		t.Fatal(err)
	}
	if after[19] != before[19] || after[29] != before[29] {
		t.Errorf("folding changed the days: %+v %+v, was %+v %+v", after[19], after[29], before[19], before[29])
	}

	// Folding a later probe of the same day adds to it.
	insert("web", fmt.Sprintf("-%d seconds", int(now.Sub(tenDays.Add(time.Hour)).Seconds())), true)
	if err := FoldHealth(database, now.Add(-7*24*time.Hour), now.Add(-90*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	again, _ := UptimeDays(database, "web", 30, now)
	if d := again[19]; d.OK != 10 || d.Total != 11 {
		t.Errorf("a day folded twice = %+v, want 10 of 11", d)
	}

	// Days past the retention are dropped.
	if err := FoldHealth(database, now.Add(-7*24*time.Hour), now.Add(-5*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if d, _ := UptimeDays(database, "web", 30, now); d[19].Total != 0 {
		t.Errorf("a day past the retention survived: %+v", d[19])
	}

	if err := DeleteAppTimeSeries(database, "web"); err != nil {
		t.Fatal(err)
	}
	if d, _ := UptimeDays(database, "web", 30, now); d[29].Total != 0 {
		t.Errorf("a deleted application kept its probes: %+v", d[29])
	}
}
