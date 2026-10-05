package db

import (
	"database/sql"
	"strings"
	"time"
)

// LogLine is one persisted line of container output, joined with the app's
// current name so cross-app search results are readable without extra lookups.
type LogLine struct {
	AppID   string
	AppName string
	// AppColor is the application's colour on the Logs page.
	AppColor string
	TS       time.Time
	Line     string
}

// maxLogLineLen caps a single stored line so one runaway line can't dominate
// storage; the live SSE view is unaffected, only the persisted copy is capped.
const maxLogLineLen = 4000

// LogEntry is one line on its way into storage, carrying the moment the
// container wrote it. A zero TS falls back to now: lines are batched and
// flushed on a timer, so the insert time can be seconds late, but it is still
// better than a row dated to the zero time.
type LogEntry struct {
	TS   time.Time
	Line string
}

// AppendLogs batches a captured chunk of an app's container output into
// storage in a single transaction.
func AppendLogs(database *sql.DB, appID string, entries []LogEntry) error {
	if len(entries) == 0 {
		return nil
	}
	tx, err := database.Begin()
	if err != nil {
		return err
	}
	// A rollback after a successful commit is a no-op, so one deferred call
	// covers every way out of here without asking which one was taken.
	defer tx.Rollback()

	stmt, err := tx.Prepare("INSERT INTO app_logs (app_id, ts, line) VALUES (?, ?, ?)")
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, e := range entries {
		if len(e.Line) > maxLogLineLen {
			e.Line = e.Line[:maxLogLineLen]
		}
		if e.TS.IsZero() {
			e.TS = time.Now()
		}
		if _, err := stmt.Exec(appID, e.TS.UTC(), e.Line); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SearchLogs returns persisted log lines, newest first, optionally scoped to
// one app and/or filtered by a substring match. An empty appID searches
// across every app; an empty query returns the most recent lines unfiltered.
// offset skips that many of the newest matches, which is how the Logs page
// reaches older ones a page at a time.
//
// The page is found first, by its rowids alone, and only then are its lines
// read: skipping to page 300 walks 30,000 index entries, not 30,000 lines of
// text joined to their application. And only the filters given are written
// into the query: a condition that holds when its parameter is empty is one
// SQLite cannot answer from an index, and it read and sorted every stored
// line for each page.
func SearchLogs(database *sql.DB, appID, query string, limit, offset int) ([]LogLine, error) {
	where, args := logFilter(appID, query)
	rows, err := database.Query(`
		SELECT app_logs.app_id, apps.name, apps.log_color, app_logs.ts, app_logs.line
		FROM app_logs
		JOIN apps ON apps.id = app_logs.app_id
		WHERE app_logs.rowid IN (
			SELECT rowid FROM app_logs`+where+`
			ORDER BY ts DESC, rowid DESC
			LIMIT ? OFFSET ?)
		ORDER BY app_logs.ts DESC, app_logs.rowid DESC`, append(args, limit, offset)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LogLine
	for rows.Next() {
		var l LogLine
		if err := rows.Scan(&l.AppID, &l.AppName, &l.AppColor, &l.TS, &l.Line); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// DeleteAppLogs removes an app's persisted log history; called when the app
// itself is deleted.
func DeleteAppLogs(database *sql.DB, appID string) error {
	_, err := database.Exec("DELETE FROM app_logs WHERE app_id = ?", appID)
	return err
}

// MaxLogRowsPerApp caps stored output per app on top of the time-based
// retention. A chatty container writes millions of lines well within the
// retention window, and this database sits on the same small disk as the
// images, the build cache and the backups.
const MaxLogRowsPerApp = 50_000

// PruneLogs enforces the per-app row cap, keeping the newest lines.
func PruneLogs(database *sql.DB) error {
	// The app IDs are collected before any delete runs: the pool is limited to
	// a single connection, so writing while a query is still open would
	// deadlock against the reader holding it.
	rows, err := database.Query("SELECT DISTINCT app_id FROM app_logs")
	if err != nil {
		return err
	}
	var appIDs []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			appIDs = append(appIDs, id)
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}

	for _, id := range appIDs {
		if _, err := database.Exec(`
			DELETE FROM app_logs WHERE id IN (
				SELECT id FROM app_logs
				WHERE app_id = ?
				ORDER BY ts DESC, id DESC
				LIMIT -1 OFFSET ?
			)`, id, MaxLogRowsPerApp); err != nil {
			return err
		}
	}
	return rows.Err()
}

// CountLogs is how many lines SearchLogs would find with no limit: what the
// Logs page needs to say how many pages there are.
func CountLogs(database *sql.DB, appID, query string) (int, error) {
	where, args := logFilter(appID, query)
	var n int
	err := database.QueryRow(`SELECT COUNT(*) FROM app_logs`+where, args...).Scan(&n)
	return n, err
}

// logFilter is the WHERE clause for the filters given, and nothing for the
// ones left empty, so that a search scoped to one application reads that
// application's index.
func logFilter(appID, query string) (string, []any) {
	var conds []string
	var args []any
	if appID != "" {
		conds = append(conds, "app_id = ?")
		args = append(args, appID)
	}
	if query != "" {
		conds = append(conds, "line LIKE '%' || ? || '%'")
		args = append(args, query)
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}
