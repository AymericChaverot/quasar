package server

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"quasar/internal/db"
)

// A share just under a hundred keeps the decimals that show it is not one.
func TestAnUptimeIsWordedToItsDecimals(t *testing.T) {
	for pct, want := range map[float64]string{
		100: "100%", 99.999: "99.99%", 99.5: "99.50%", 98.97: "98.9%", 97.25: "97.2%",
	} {
		if got := uptimePercent(pct); got != want {
			t.Errorf("uptimePercent(%v) = %q, want %q", pct, got, want)
		}
	}
	for pct, want := range map[float64]string{-1: "none", 100: "ok", 99.5: "ok", 99: "warn", 94: "err"} {
		if got := uptimeTone(pct); got != want {
			t.Errorf("uptimeTone(%v) = %s, want %s", pct, got, want)
		}
	}
}

// The card draws a bar a day and words today, the week, the month and the
// quarter from the checks that ran.
func TestTheUptimeCardCountsTheChecks(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "quasar.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	if v := appUptime(database, "web", false, time.Now()); !v.Empty || len(v.Bars) != uptimeDays {
		t.Fatalf("an application never checked = %+v", v)
	}

	// Today: three checks, one failed. Three days ago: two, both answered.
	for i, row := range []struct {
		ago string
		ok  bool
	}{{"-1 seconds", true}, {"-2 seconds", true}, {"-3 seconds", false}, {"-3 days", true}, {"-3 days", true}} {
		if _, err := database.Exec(`INSERT INTO health_history (app_id, ts, ok) VALUES ('web', datetime('now', ?, ?), ?)`,
			row.ago, fmt.Sprintf("-%d seconds", i), row.ok); err != nil {
			t.Fatal(err)
		}
	}
	v := appUptime(database, "web", true, time.Now())
	if v.Empty {
		t.Fatal("checks were recorded and the card is empty")
	}
	today, past := v.Bars[uptimeDays-1], v.Bars[uptimeDays-4]
	if today.Tone != "err" || !strings.Contains(today.Title, "1 of 3 checks failed") {
		t.Errorf("today = %+v", today)
	}
	if past.Tone != "ok" || !strings.Contains(past.Title, "all checks answered") {
		t.Errorf("three days ago = %+v", past)
	}
	if v.Bars[0].Tone != "none" || !strings.HasSuffix(v.Bars[0].Title, "no checks") {
		t.Errorf("a day without checks = %+v", v.Bars[0])
	}
	want := []string{"Today 66.6%", "7 days 80.0%", "30 days 80.0%", "90 days 80.0%"}
	for i, f := range v.Figures {
		if got := f.Label + " " + f.Value; got != want[i] {
			t.Errorf("figure %d = %q, want %q", i, got, want[i])
		}
	}
}
