package db

import (
	"testing"
	"time"
)

// The published applications are replaced as a whole, and go with an
// application that is deleted.
func TestStatusAppsAreReplacedAndForgotten(t *testing.T) {
	database := openTestDB(t)
	if err := SetStatusApps(database, []StatusApp{{"a", "Cloud"}, {"b", "Photos"}}); err != nil {
		t.Fatal(err)
	}
	if err := SetStatusApps(database, []StatusApp{{"b", "Pictures"}}); err != nil {
		t.Fatal(err)
	}
	got, err := StatusApps(database)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got["b"] != "Pictures" {
		t.Errorf("published = %v, want only b as Pictures", got)
	}
	if err := DeleteAppTimeSeries(database, "b"); err != nil {
		t.Fatal(err)
	}
	if got, _ := StatusApps(database); len(got) != 0 {
		t.Errorf("a deleted application is still published: %v", got)
	}
}

// An incident starts at the first failed check since the last answered one,
// is opened once however many checks fail, and ends when one answers.
func TestAnIncidentRunsFromTheFirstFailureToTheNextAnswer(t *testing.T) {
	database := openTestDB(t)
	for _, row := range []struct {
		ago string
		ok  bool
	}{{"-10 minutes", true}, {"-5 minutes", false}, {"-4 minutes", false}} {
		if _, err := database.Exec(`INSERT INTO health_history (app_id, ts, ok) VALUES ('web', datetime('now', ?), ?)`,
			row.ago, row.ok); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if err := OpenIncident(database, "web"); err != nil {
			t.Fatal(err)
		}
	}
	if err := MarkIncidentRestarted(database, "web"); err != nil {
		t.Fatal(err)
	}
	open, err := Incidents(database, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 1 || open[0].Ended != nil || !open[0].Restarted {
		t.Fatalf("open incidents = %+v, want one, restarted, still open", open)
	}
	if ago := time.Since(open[0].Started); ago < 4*time.Minute+50*time.Second || ago > 5*time.Minute+10*time.Second {
		t.Errorf("the incident started %s ago, want five minutes: the first failure", ago)
	}

	if err := CloseIncident(database, "web"); err != nil {
		t.Fatal(err)
	}
	closed, _ := Incidents(database, time.Now().Add(-time.Hour))
	if len(closed) != 1 || closed[0].Ended == nil {
		t.Fatalf("after closing = %+v", closed)
	}
	// A closed incident is past: the next failure opens another.
	if err := OpenIncident(database, "web"); err != nil {
		t.Fatal(err)
	}
	if again, _ := Incidents(database, time.Now().Add(-time.Hour)); len(again) != 2 {
		t.Errorf("%d incidents, want a second one", len(again))
	}
	// Incidents that ended before the window are not in it; open ones always are.
	if old, _ := Incidents(database, time.Now().Add(time.Hour)); len(old) != 1 || old[0].Ended != nil {
		t.Errorf("incidents in a window after both = %+v, want only the open one", old)
	}
}
