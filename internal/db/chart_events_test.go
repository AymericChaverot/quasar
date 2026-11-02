package db

import (
	"fmt"
	"testing"
	"time"
)

// A chart of the server marks its own events and every deployment, named
// after the application, oldest first.
func TestChartEventsReadDeploymentsBesideTheirOwn(t *testing.T) {
	database := openTestDB(t)
	keyring := testKeyring(t)
	if err := InsertApp(database, keyring, &App{ID: "a1", Name: "blog", Subdomain: "blog", DeployType: "image", ImageRef: "nginx"}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO deployments (app_id, started_at) VALUES ('a1', datetime('now', '-2 hours'))`); err != nil {
		t.Fatal(err)
	}
	if err := RecordChartEvent(database, EventRestart, "a1", "Auto-restart · blog"); err != nil {
		t.Fatal(err)
	}
	// Too old for the window.
	if _, err := database.Exec(`INSERT INTO chart_events (ts, kind, label) VALUES (datetime('now', '-3 days'), 'alert', 'old')`); err != nil {
		t.Fatal(err)
	}

	got, err := ChartEvents(database, time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d events, want 2: %+v", len(got), got)
	}
	if got[0].Kind != EventDeploy || got[0].Label != "Deploy · blog" || got[1].Kind != EventRestart {
		t.Errorf("events = %+v; want the deployment, then the restart", got)
	}
	if ago := time.Since(got[0].At); ago < 119*time.Minute || ago > 121*time.Minute {
		t.Errorf("the deployment is placed %s ago, want two hours", ago)
	}
}

// The cap keeps the newest, not the oldest.
func TestChartEventsKeepTheNewestWhenThereAreTooMany(t *testing.T) {
	database := openTestDB(t)
	for i := range maxChartEvents + 5 {
		if _, err := database.Exec(`INSERT INTO chart_events (ts, kind, label) VALUES (datetime('now', ?), 'alert', ?)`,
			fmt.Sprintf("-%d seconds", maxChartEvents+5-i), fmt.Sprint(i)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := ChartEvents(database, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != maxChartEvents || got[len(got)-1].Label != "44" {
		t.Errorf("kept %d events ending with %q; want %d ending with the newest", len(got), got[len(got)-1].Label, maxChartEvents)
	}
}
