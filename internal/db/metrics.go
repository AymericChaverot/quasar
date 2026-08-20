package db

import (
	"database/sql"
	"time"
)

// --- Health history -----------------------------------------------------------

// RecordHealth stores the result of one health probe.
//
// The error is returned rather than swallowed because these writes are the
// only evidence the platform keeps of an app's history: a database that has
// been refusing them for a week looks exactly like an app nobody has probed,
// and the caller is where a log line can say which it is.
func RecordHealth(db *sql.DB, appID string, ok bool) error {
	_, err := db.Exec("INSERT INTO health_history (app_id, ok) VALUES (?, ?)", appID, ok)
	return err
}

// ConsecutiveHealthFailures counts failures since the last success. A query
// that will not run reads as no failures, which is what the caller does with
// an app it has no history for anyway.
func ConsecutiveHealthFailures(db *sql.DB, appID string) int {
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM health_history
		WHERE app_id = ? AND ok = 0
		AND ts > COALESCE((SELECT MAX(ts) FROM health_history WHERE app_id = ? AND ok = 1), 0)`,
		appID, appID).Scan(&n); err != nil {
		return 0
	}
	return n
}

// --- Metrics samples ----------------------------------------------------------

type MetricPoint struct {
	TS time.Time
	V1 float64 // cpu %
	V2 float64 // mem % (server) or mem MB (app)
}

func RecordServerMetric(db *sql.DB, cpu, mem, disk float64) error {
	_, err := db.Exec("INSERT INTO metrics (cpu, mem, disk) VALUES (?, ?, ?)", cpu, mem, disk)
	return err
}

func RecordAppMetric(db *sql.DB, appID string, cpu, memMB float64) error {
	_, err := db.Exec("INSERT INTO app_metrics (app_id, cpu, mem_mb) VALUES (?, ?, ?)", appID, cpu, memMB)
	return err
}

func ServerMetrics(db *sql.DB, since time.Time) ([]MetricPoint, error) {
	return queryPoints(db, "SELECT ts, cpu, mem FROM metrics WHERE ts >= ? ORDER BY ts", since)
}

func AppMetrics(db *sql.DB, appID string, since time.Time) ([]MetricPoint, error) {
	return queryPoints(db, "SELECT ts, cpu, mem_mb FROM app_metrics WHERE app_id = ? AND ts >= ? ORDER BY ts", appID, since)
}

func queryPoints(db *sql.DB, query string, args ...any) ([]MetricPoint, error) {
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MetricPoint
	for rows.Next() {
		var p MetricPoint
		if err := rows.Scan(&p.TS, &p.V1, &p.V2); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// PruneTimeSeries drops samples older than the retention window, reporting the
// first table it could not trim: one locked database fails all four the same
// way, and four copies of that in the log say nothing the first did not.
func PruneTimeSeries(db *sql.DB, olderThan time.Time) error {
	return firstError(
		exec(db, "DELETE FROM metrics WHERE ts < ?", olderThan),
		exec(db, "DELETE FROM app_metrics WHERE ts < ?", olderThan),
		exec(db, "DELETE FROM health_history WHERE ts < ?", olderThan),
		exec(db, "DELETE FROM app_logs WHERE ts < ?", olderThan),
	)
}

// Reclaim gives the disk back the space deleted rows left behind, returning
// how many bytes were recovered (0 when it chose not to run).
//
// Deleting rows only moves their pages onto SQLite's free list for reuse, so a
// rolling retention window never shrinks the file: it grows to its high-water
// mark and stays there. VACUUM rewrites the database to compact it, which
// needs room for a second copy and blocks other queries, so it only runs once
// there is a worthwhile amount to recover.
func Reclaim(db *sql.DB) int64 {
	var pages, free, pageSize int64
	if db.QueryRow("PRAGMA page_count").Scan(&pages) != nil || pages == 0 {
		return 0
	}
	if db.QueryRow("PRAGMA freelist_count").Scan(&free) != nil {
		return 0
	}
	if db.QueryRow("PRAGMA page_size").Scan(&pageSize) != nil {
		return 0
	}
	recoverable := free * pageSize
	if free*100/pages < 20 || recoverable < 16<<20 {
		return 0
	}
	if _, err := db.Exec("VACUUM"); err != nil {
		return 0
	}
	return recoverable
}

func DeleteAppTimeSeries(db *sql.DB, appID string) error {
	return firstError(
		exec(db, "DELETE FROM app_metrics WHERE app_id = ?", appID),
		exec(db, "DELETE FROM health_history WHERE app_id = ?", appID),
	)
}

// exec runs a statement whose result row count nobody wants.
func exec(db *sql.DB, query string, args ...any) error {
	_, err := db.Exec(query, args...)
	return err
}

// firstError is the first of several attempts that failed, or nil.
func firstError(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}
