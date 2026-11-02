package monitor

import (
	"path/filepath"
	"testing"
	"time"

	"quasar/internal/db"
)

// One failed check is a timeout, not an incident; the second opens one, and
// the next check that answers closes it.
func TestChecksOpenAndCloseAnIncident(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	incidents := func() []db.Incident {
		t.Helper()
		in, err := db.Incidents(database, time.Now().Add(-time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		return in
	}

	// The checks run thirty seconds apart; the last answer is put a minute back
	// so the failures after it are not in the same second.
	if _, err := database.Exec(`INSERT INTO health_history (app_id, ts, ok) VALUES ('web', datetime('now', '-1 minutes'), 1)`); err != nil {
		t.Fatal(err)
	}
	if fails := recordCheck(database, "web", false); fails != 1 || len(incidents()) != 0 {
		t.Fatalf("one failed check: %d failures, incidents %+v", fails, incidents())
	}
	if fails := recordCheck(database, "web", false); fails != 2 {
		t.Fatalf("%d failures after two", fails)
	}
	if in := incidents(); len(in) != 1 || in[0].Ended != nil {
		t.Fatalf("after two failed checks = %+v, want one open incident", in)
	}
	recordCheck(database, "web", false)
	if in := incidents(); len(in) != 1 {
		t.Fatalf("a third failure opened another incident: %+v", in)
	}
	if fails := recordCheck(database, "web", true); fails != 0 {
		t.Fatalf("%d failures after an answer", fails)
	}
	if in := incidents(); len(in) != 1 || in[0].Ended == nil {
		t.Errorf("after an answer = %+v, want the incident closed", in)
	}
}
