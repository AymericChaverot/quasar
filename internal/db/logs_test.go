package db

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"quasar/internal/secrets"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	database, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

func testKeyring(t *testing.T) *secrets.Keyring {
	t.Helper()
	k, err := secrets.LoadOrCreateKey(filepath.Join(t.TempDir(), "master.key"))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestAppendAndSearchLogs(t *testing.T) {
	database := openTestDB(t)
	keyring := testKeyring(t)

	// SearchLogs joins against apps, so rows need a real app to show up.
	if err := InsertApp(database, keyring, &App{ID: "app1", Name: "Web", Subdomain: "web", DeployType: "image", ImageRef: "nginx"}); err != nil {
		t.Fatal(err)
	}
	if err := InsertApp(database, keyring, &App{ID: "app2", Name: "Worker", Subdomain: "worker", DeployType: "image", ImageRef: "worker"}); err != nil {
		t.Fatal(err)
	}

	AppendLogs(database, "app1", []LogEntry{{Line: "hello world"}, {Line: "an ERROR occurred"}})
	AppendLogs(database, "app2", []LogEntry{{Line: "another error here"}})

	all, err := SearchLogs(database, "", "error", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("cross-app search for \"error\": got %d results, want 2: %+v", len(all), all)
	}

	scoped, err := SearchLogs(database, "app1", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(scoped) != 2 {
		t.Fatalf("scoped to app1: got %d results, want 2", len(scoped))
	}
	for _, l := range scoped {
		if l.AppID != "app1" {
			t.Errorf("got AppID %q, want app1", l.AppID)
		}
		if l.AppName != "Web" {
			t.Errorf("got AppName %q, want Web", l.AppName)
		}
	}

	DeleteAppLogs(database, "app1")
	remaining, err := SearchLogs(database, "", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 1 || remaining[0].AppID != "app2" {
		t.Fatalf("after DeleteAppLogs(app1): got %+v, want only app2's line", remaining)
	}
}

// History is stored under the moment the container wrote a line, not the
// moment the collector flushed its batch. The two differ by up to the flush
// interval — and by the entire backlog when a stream opens on a container that
// has been running for days, which would otherwise land every one of those
// lines under the same instant.
func TestAppendLogsKeepsTheContainersTimestamp(t *testing.T) {
	database := openTestDB(t)
	if err := InsertApp(database, testKeyring(t), &App{ID: "app1", Name: "Web", Subdomain: "web", DeployType: "image", ImageRef: "nginx"}); err != nil {
		t.Fatal(err)
	}

	emitted := time.Date(2026, 7, 28, 23, 31, 1, 0, time.UTC)
	AppendLogs(database, "app1", []LogEntry{
		{TS: emitted, Line: "from the container's clock"},
		{Line: "unstamped"}, // no timestamp: falls back to now
	})

	got, err := SearchLogs(database, "app1", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d rows, want 2", len(got))
	}

	byLine := map[string]time.Time{}
	for _, l := range got {
		byLine[l.Line] = l.TS
	}
	if ts := byLine["from the container's clock"]; !ts.UTC().Equal(emitted) {
		t.Errorf("stored TS = %s, want the container's %s", ts.UTC(), emitted)
	}
	if ts := byLine["unstamped"]; time.Since(ts) > time.Minute {
		t.Errorf("an unstamped line got %s, want roughly now", ts)
	}
}

func TestAppendLogsTruncatesLongLines(t *testing.T) {
	database := openTestDB(t)
	if err := InsertApp(database, testKeyring(t), &App{ID: "app1", Name: "Web", Subdomain: "web", DeployType: "image", ImageRef: "nginx"}); err != nil {
		t.Fatal(err)
	}

	long := make([]byte, maxLogLineLen+500)
	for i := range long {
		long[i] = 'x'
	}
	AppendLogs(database, "app1", []LogEntry{{Line: string(long)}})

	got, err := SearchLogs(database, "app1", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d rows, want 1", len(got))
	}
	if len(got[0].Line) != maxLogLineLen {
		t.Errorf("stored line length = %d, want %d", len(got[0].Line), maxLogLineLen)
	}
}
