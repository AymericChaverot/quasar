package db

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestParseLogQueryReadsWhatWasTyped(t *testing.T) {
	cases := []struct {
		in   string
		want parsedLogQuery
	}{
		{"connection refused", parsedLogQuery{any: [][]logTerm{{{text: "connection"}, {text: "refused"}}}}},
		{`"expired session" user`, parsedLogQuery{any: [][]logTerm{{{text: "expired session", phrase: true}, {text: "user"}}}}},
		{"timeout OR refused", parsedLogQuery{any: [][]logTerm{{{text: "timeout"}}, {{text: "refused"}}}}},
		{"error -healthcheck", parsedLogQuery{any: [][]logTerm{{{text: "error"}}}, none: []logTerm{{text: "healthcheck"}}}},
		{`-"GET /health"`, parsedLogQuery{none: []logTerm{{text: "GET /health", phrase: true}}}},
		// Forgiving: a quote left open runs to the end, a lone dash is a word,
		// and an OR with nothing after it is dropped.
		{`"left open`, parsedLogQuery{any: [][]logTerm{{{text: "left open", phrase: true}}}}},
		{"a - b", parsedLogQuery{any: [][]logTerm{{{text: "a"}, {text: "-"}, {text: "b"}}}}},
		{"error OR", parsedLogQuery{any: [][]logTerm{{{text: "error"}}}}},
		{`  ""  `, parsedLogQuery{}},
	}
	for _, tc := range cases {
		if got := parseLogQuery(tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("parseLogQuery(%q) = %+v, want %+v", tc.in, got, tc.want)
		}
	}
}

// Every search gives the same lines whether the index answers it or the lines
// are read, for what the two can both find: the index is only ever a faster
// way to the same answer.
func TestLogSearchFindsTheSameLinesWithAndWithoutTheIndex(t *testing.T) {
	d := openTestDB(t)
	k := testKeyring(t)
	for _, id := range []string{"app1", "app2"} {
		if err := InsertApp(d, k, &App{ID: id, Name: id, Subdomain: id, DeployType: "image", ImageRef: "nginx"}); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-48 * time.Hour)
	AppendLogs(d, "app1", []LogEntry{
		{TS: old, Line: "ERROR connection refused by upstream"},
		{Line: "GET /health 200"},
		{Line: "worker job completed in 12 ms"},
		{Line: "Traceback (most recent call last):"},
		{Line: "session expired for user 7 -> redirect"},
	})
	AppendLogs(d, "app2", []LogEntry{
		{Line: "connection timeout"},
		{Line: "panic: runtime error: index out of range"},
		{Line: "GET /api/users 200"},
	})

	cases := []struct {
		q    LogQuery
		want int
	}{
		{LogQuery{Text: "connection"}, 2},
		{LogQuery{Text: "connection refused"}, 1},
		{LogQuery{Text: "refused OR timeout"}, 2},
		{LogQuery{Text: `"expired for user"`}, 1},
		{LogQuery{Text: "GET -health"}, 1},
		{LogQuery{Text: "-GET"}, 6},
		{LogQuery{Text: "->"}, 1},
		{LogQuery{Text: "connection", App: "app2"}, 1},
		{LogQuery{Text: "connection", Since: time.Now().Add(-time.Hour)}, 1},
		{LogQuery{Errors: true}, 3},
		{LogQuery{Errors: true, Text: "-panic"}, 2},
	}
	for _, tc := range cases {
		for _, indexed := range []bool{true, false} {
			where, args := tc.q.where(indexed)
			var n int
			if err := d.QueryRow("SELECT COUNT(*) FROM app_logs"+where, args...).Scan(&n); err != nil {
				t.Fatalf("%+v (index %v): %v", tc.q, indexed, err)
			}
			if n != tc.want {
				t.Errorf("%+v (index %v): %d lines, want %d", tc.q, indexed, n, tc.want)
			}
		}
	}

	// A word typed as its start is found by the index, which the lines alone
	// would find as well — but a piece from the middle of a word only reading
	// the lines finds, which is what Exact is for.
	if n, _ := CountLogs(d, LogQuery{Text: "refus"}); n != 1 {
		t.Errorf("the start of a word found %d lines, want 1", n)
	}
	if n, _ := CountLogs(d, LogQuery{Text: "efuse"}); n != 0 {
		t.Errorf("the middle of a word found %d lines, want none without Exact", n)
	}
	if n, _ := CountLogs(d, LogQuery{Text: "efuse", Exact: true}); n != 1 {
		t.Errorf("an exact search found %d lines, want 1", n)
	}
}

// A line leaves the index with the line: a deleted line is never found again.
func TestADeletedLogLineLeavesTheIndex(t *testing.T) {
	d := openTestDB(t)
	k := testKeyring(t)
	if err := InsertApp(d, k, &App{ID: "app1", Name: "app1", Subdomain: "a", DeployType: "image", ImageRef: "nginx"}); err != nil {
		t.Fatal(err)
	}
	AppendLogs(d, "app1", []LogEntry{{Line: "needle in the haystack"}})
	if n, _ := CountLogs(d, LogQuery{Text: "needle"}); n != 1 {
		t.Fatalf("found %d lines before the purge", n)
	}
	if _, err := PurgeLogs(d); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := d.QueryRow("SELECT COUNT(*) FROM app_logs_fts WHERE app_logs_fts MATCH 'needle'").Scan(&n); err != nil || n != 0 {
		t.Errorf("the index still holds %d lines (%v)", n, err)
	}
}

// A database whose lines were stored before the index existed gets them into
// it, a batch at a time, and is searched by reading the lines until it has.
func TestLinesStoredBeforeTheIndexAreAddedToIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	// The database as an install before the index left it.
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`CREATE TABLE app_logs (id INTEGER PRIMARY KEY AUTOINCREMENT, app_id TEXT NOT NULL,
		ts DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, line TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < ?)
		INSERT INTO app_logs (app_id, line) SELECT 'app1', 'stored before the index' FROM n`, logIndexBatch+5); err != nil {
		t.Fatal(err)
	}
	raw.Close()

	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if logIndexReady(d) {
		t.Fatal("the index says it is whole before anything was added to it")
	}
	// A line written now goes in through the trigger, and is not added twice.
	AppendLogs(d, "app1", []LogEntry{{Line: "written after the index"}})
	// One of the old lines goes before the backfill reaches it.
	if _, err := d.Exec("DELETE FROM app_logs WHERE id = 3"); err != nil {
		t.Fatal(err)
	}

	added, err := BackfillLogIndex(d, 0)
	if err != nil {
		t.Fatal(err)
	}
	if added != logIndexBatch+4 {
		t.Errorf("added %d lines, want %d", added, logIndexBatch+4)
	}
	if !logIndexReady(d) {
		t.Error("the index is not marked whole")
	}
	var n int
	d.QueryRow("SELECT COUNT(*) FROM app_logs_fts WHERE app_logs_fts MATCH 'stored'").Scan(&n)
	if n != logIndexBatch+4 {
		t.Errorf("the index finds %d old lines, want %d", n, logIndexBatch+4)
	}
	d.QueryRow("SELECT COUNT(*) FROM app_logs_fts WHERE app_logs_fts MATCH 'written'").Scan(&n)
	if n != 1 {
		t.Errorf("the index finds the new line %d times, want once", n)
	}
	// Run again, it has nothing to do.
	if added, err := BackfillLogIndex(d, 0); err != nil || added != 0 {
		t.Errorf("a second run added %d (%v)", added, err)
	}
}

// The lines around one are its own application's, in order, the line among
// them.
func TestLogContextIsTheLinesAroundOne(t *testing.T) {
	d := openTestDB(t)
	k := testKeyring(t)
	for _, id := range []string{"app1", "app2"} {
		if err := InsertApp(d, k, &App{ID: id, Name: id, Subdomain: id, DeployType: "image", ImageRef: "nginx"}); err != nil {
			t.Fatal(err)
		}
	}
	at := time.Now().Add(-time.Minute)
	var entries []LogEntry
	for i := 0; i < 10; i++ {
		// Two lines to a second: the order inside one is the id's.
		entries = append(entries, LogEntry{TS: at.Add(time.Duration(i/2) * time.Second), Line: string(rune('a' + i))})
	}
	AppendLogs(d, "app1", entries)
	AppendLogs(d, "app2", []LogEntry{{TS: at, Line: "other"}})

	var id int64
	d.QueryRow("SELECT id FROM app_logs WHERE line = 'e'").Scan(&id)
	lines, err := LogContext(d, id, 2)
	if err != nil {
		t.Fatal(err)
	}
	var got string
	for _, l := range lines {
		got += l.Line
	}
	if got != "cdefg" {
		t.Errorf("context = %q, want cdefg", got)
	}
	if lines, err := LogContext(d, 999999, 2); err != nil || lines != nil {
		t.Errorf("a line no longer stored gave %v, %v", lines, err)
	}
}
