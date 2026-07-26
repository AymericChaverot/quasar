package db

import (
	"database/sql"
	"fmt"
	"testing"
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
	AppendLogs(database, "quiet", []string{"only line"})

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
