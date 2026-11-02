package db

import (
	"database/sql"
	"path/filepath"
	"strings"
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

	all, err := SearchLogs(database, LogQuery{App: "", Text: "error"}, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("cross-app search for \"error\": got %d results, want 2: %+v", len(all), all)
	}

	scoped, err := SearchLogs(database, LogQuery{App: "app1", Text: ""}, 10, 0)
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
	remaining, err := SearchLogs(database, LogQuery{App: "", Text: ""}, 10, 0)
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

	got, err := SearchLogs(database, LogQuery{App: "app1", Text: ""}, 10, 0)
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

	got, err := SearchLogs(database, LogQuery{App: "app1", Text: ""}, 10, 0)
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

// Lines written in the same instant still page in a fixed order: without a
// tiebreak, an offset could show one of them twice and skip another.
func TestSearchLogsPagesLinesOfTheSameInstant(t *testing.T) {
	database := openTestDB(t)
	if err := InsertApp(database, testKeyring(t), &App{ID: "app1", Name: "Web", Subdomain: "web", DeployType: "image", ImageRef: "nginx"}); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	var entries []LogEntry
	for _, l := range []string{"1", "2", "3", "4", "5"} {
		entries = append(entries, LogEntry{TS: at, Line: l})
	}
	if err := AppendLogs(database, "app1", entries); err != nil {
		t.Fatal(err)
	}
	var seen []string
	for offset := 0; offset < 6; offset += 2 {
		page, err := SearchLogs(database, LogQuery{App: "", Text: ""}, 2, offset)
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range page {
			seen = append(seen, l.Line)
		}
	}
	if got := strings.Join(seen, ""); got != "54321" {
		t.Errorf("pages read %q, want every line once, newest first (54321)", got)
	}
}

// The count agrees with what the search finds, scope and filter included.
func TestCountLogsMatchesTheSearch(t *testing.T) {
	database := openTestDB(t)
	k := testKeyring(t)
	for _, id := range []string{"app1", "app2"} {
		if err := InsertApp(database, k, &App{ID: id, Name: id, Subdomain: id, DeployType: "image", ImageRef: "nginx"}); err != nil {
			t.Fatal(err)
		}
	}
	AppendLogs(database, "app1", []LogEntry{{Line: "hello"}, {Line: "an error"}, {Line: "disk at 50% for user_id 7"}})
	AppendLogs(database, "app2", []LogEntry{{Line: "another error"}, {Line: "disk at 500 for userXid"}})
	for _, tc := range []struct {
		app, query string
		exact      bool
		want       int
	}{
		{"", "", false, 5}, {"app1", "", false, 3}, {"", "error", false, 2}, {"app2", "hello", false, 0},
		// A word is the start of one: 50 is in "50%" and in "500".
		{"", "50", false, 2}, {"", "user_id", false, 1},
		// Wildcards in an exact search are the characters themselves.
		{"", "50%", true, 1}, {"", "user_id", true, 1}, {"", `\`, true, 0},
	} {
		q := LogQuery{App: tc.app, Text: tc.query, Exact: tc.exact}
		if n, err := CountLogs(database, q); err != nil || n != tc.want {
			t.Errorf("CountLogs(%+v) = %d, %v; want %d", q, n, err, tc.want)
		}
		if lines, err := SearchLogs(database, q, 10, 0); err != nil || len(lines) != tc.want {
			t.Errorf("SearchLogs(%+v) = %d lines, %v; want %d", q, len(lines), err, tc.want)
		}
	}
}

// A page of logs is read from an index, not by sorting every stored line: with
// every application at once, and with one, the plan has no sort of its own.
// Fifty thousand lines per application made each page of the Logs page read
// and sort all of them.
func TestAPageOfLogsIsReadFromAnIndex(t *testing.T) {
	d := openTestDB(t)
	since := time.Now().Add(-time.Hour)
	for _, q := range []LogQuery{{}, {App: "app1"}, {Since: since}, {App: "app1", Since: since}} {
		where, args := q.where(true)
		rows, err := d.Query(`EXPLAIN QUERY PLAN SELECT rowid FROM app_logs`+where+`
			ORDER BY ts DESC, rowid DESC LIMIT 100 OFFSET 0`, args...)
		if err != nil {
			t.Fatal(err)
		}
		var plan []string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			plan = append(plan, detail)
		}
		rows.Close()
		got := strings.Join(plan, "; ")
		if strings.Contains(got, "TEMP B-TREE") || !strings.Contains(got, "INDEX") {
			t.Errorf("%+v: %s", q, got)
		}
	}
}
