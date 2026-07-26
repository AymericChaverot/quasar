package db

import (
	"database/sql"
	"time"
)

// --- Health history -----------------------------------------------------------

func RecordHealth(db *sql.DB, appID string, ok bool) {
	db.Exec("INSERT INTO health_history (app_id, ok) VALUES (?, ?)", appID, ok)
}

// Uptime returns the success ratio (0–100) and check count over a window.
func Uptime(db *sql.DB, appID string, since time.Time) (float64, int) {
	var total, up int
	db.QueryRow("SELECT COUNT(*), COALESCE(SUM(ok), 0) FROM health_history WHERE app_id = ? AND ts >= ?",
		appID, since).Scan(&total, &up)
	if total == 0 {
		return 0, 0
	}
	return float64(up) / float64(total) * 100, total
}

// ConsecutiveHealthFailures counts failures since the last success.
func ConsecutiveHealthFailures(db *sql.DB, appID string) int {
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM health_history
		WHERE app_id = ? AND ok = 0
		AND ts > COALESCE((SELECT MAX(ts) FROM health_history WHERE app_id = ? AND ok = 1), 0)`,
		appID, appID).Scan(&n)
	return n
}

// --- Metrics samples ----------------------------------------------------------

type MetricPoint struct {
	TS   time.Time
	V1   float64 // cpu %
	V2   float64 // mem % (server) or mem MB (app)
}

func RecordServerMetric(db *sql.DB, cpu, mem, disk float64) {
	db.Exec("INSERT INTO metrics (cpu, mem, disk) VALUES (?, ?, ?)", cpu, mem, disk)
}

func RecordAppMetric(db *sql.DB, appID string, cpu, memMB float64) {
	db.Exec("INSERT INTO app_metrics (app_id, cpu, mem_mb) VALUES (?, ?, ?)", appID, cpu, memMB)
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

// PruneTimeSeries drops samples older than the retention window.
func PruneTimeSeries(db *sql.DB, olderThan time.Time) {
	db.Exec("DELETE FROM metrics WHERE ts < ?", olderThan)
	db.Exec("DELETE FROM app_metrics WHERE ts < ?", olderThan)
	db.Exec("DELETE FROM health_history WHERE ts < ?", olderThan)
	db.Exec("DELETE FROM app_logs WHERE ts < ?", olderThan)
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

func DeleteAppTimeSeries(db *sql.DB, appID string) {
	db.Exec("DELETE FROM app_metrics WHERE app_id = ?", appID)
	db.Exec("DELETE FROM health_history WHERE app_id = ?", appID)
}
