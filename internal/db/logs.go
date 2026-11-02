package db

import (
	"database/sql"
	"time"
)

// LogLine is one persisted line of container output, joined with the app's
// current name so cross-app search results are readable without extra lookups.
type LogLine struct {
	// ID is the line's place in storage, which is how the lines around it
	// are found.
	ID      int64
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

// SearchLogs returns the stored lines q matches, newest first. offset skips
// that many of the newest matches, which is how the Logs page reaches older
// ones a page at a time.
//
// The page is found first, by its rowids alone, and only then are its lines
// read: skipping to page 300 walks 30,000 index entries, not 30,000 lines of
// text joined to their application.
func SearchLogs(database *sql.DB, q LogQuery, limit, offset int) ([]LogLine, error) {
	where, args := q.where(logIndexReady(database))
	rows, err := database.Query(`
		SELECT app_logs.id, app_logs.app_id, apps.name, apps.log_color, app_logs.ts, app_logs.line
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
	return scanLogLines(rows)
}

func scanLogLines(rows *sql.Rows) ([]LogLine, error) {
	defer rows.Close()
	var out []LogLine
	for rows.Next() {
		var l LogLine
		if err := rows.Scan(&l.ID, &l.AppID, &l.AppName, &l.AppColor, &l.TS, &l.Line); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// LogContext is the line id and the lines its application wrote just before
// and just after it — before of them and after of them — oldest first: what
// was going on around an error. The line itself is among them; nil when it is
// no longer stored.
func LogContext(database *sql.DB, id int64, before, after int) ([]LogLine, error) {
	var app string
	var ts time.Time
	if err := database.QueryRow("SELECT app_id, ts FROM app_logs WHERE id = ?", id).Scan(&app, &ts); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	// Two reads of the (app_id, ts) index, one each way from the line. Lines
	// from one moment share a timestamp, and the id orders them, as it does
	// everywhere else on the page.
	rows, err := database.Query(`
		SELECT * FROM (
			SELECT l.id, l.app_id, a.name, a.log_color, l.ts, l.line FROM app_logs l JOIN apps a ON a.id = l.app_id
			WHERE l.app_id = ? AND (l.ts < ? OR (l.ts = ? AND l.id < ?))
			ORDER BY l.ts DESC, l.id DESC LIMIT ?)
		UNION ALL
		SELECT * FROM (
			SELECT l.id, l.app_id, a.name, a.log_color, l.ts, l.line FROM app_logs l JOIN apps a ON a.id = l.app_id
			WHERE l.app_id = ? AND (l.ts > ? OR (l.ts = ? AND l.id >= ?))
			ORDER BY l.ts, l.id LIMIT ?)
		ORDER BY 5, 1`, app, ts, ts, id, before, app, ts, ts, id, after+1)
	if err != nil {
		return nil, err
	}
	return scanLogLines(rows)
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
	// The app IDs are collected before any delete runs, so no delete is made
	// under a read still in progress.
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
func CountLogs(database *sql.DB, q LogQuery) (int, error) {
	where, args := q.where(logIndexReady(database))
	var n int
	err := database.QueryRow(`SELECT COUNT(*) FROM app_logs`+where, args...).Scan(&n)
	return n, err
}
