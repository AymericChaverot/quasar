package db

import (
	"database/sql"
	"path/filepath"
	"testing"
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

func TestAppendAndSearchLogs(t *testing.T) {
	database := openTestDB(t)

	// SearchLogs joins against apps, so rows need a real app to show up.
	if err := InsertApp(database, &App{ID: "app1", Name: "Web", Subdomain: "web", DeployType: "image", ImageRef: "nginx"}); err != nil {
		t.Fatal(err)
	}
	if err := InsertApp(database, &App{ID: "app2", Name: "Worker", Subdomain: "worker", DeployType: "image", ImageRef: "worker"}); err != nil {
		t.Fatal(err)
	}

	AppendLogs(database, "app1", []string{"hello world", "an ERROR occurred"})
	AppendLogs(database, "app2", []string{"another error here"})

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

func TestAppendLogsTruncatesLongLines(t *testing.T) {
	database := openTestDB(t)
	if err := InsertApp(database, &App{ID: "app1", Name: "Web", Subdomain: "web", DeployType: "image", ImageRef: "nginx"}); err != nil {
		t.Fatal(err)
	}

	long := make([]byte, maxLogLineLen+500)
	for i := range long {
		long[i] = 'x'
	}
	AppendLogs(database, "app1", []string{string(long)})

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
