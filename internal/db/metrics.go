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
	V3 float64 // disk % (server only)
}

func RecordServerMetric(db *sql.DB, cpu, mem, disk float64) error {
	_, err := db.Exec("INSERT INTO metrics (cpu, mem, disk) VALUES (?, ?, ?)", cpu, mem, disk)
	return err
}

func RecordAppMetric(db *sql.DB, appID string, cpu, memMB float64) error {
	_, err := db.Exec("INSERT INTO app_metrics (app_id, cpu, mem_mb) VALUES (?, ?, ?)", appID, cpu, memMB)
	return err
}

// A window is moved to UTC before it is asked about, because that is the zone
// the samples are written in: every ts here comes from SQLite's own
// CURRENT_TIMESTAMP. A caller that says "the last 24 hours" in local time is
// asking a question about a different 24 hours — two hours short of them in
// Paris in summer, and on a server far enough east of UTC, a window that ended
// before the newest sample was taken and a graph that is simply empty. The
// callers are all "the last day of it" and none of them means anything by the
// zone, so the correction belongs here rather than at each of them.

// Both windows are averaged into buckets by the query rather than read whole
// and averaged afterwards. The monitor writes a sample a minute, so a day is
// 1440 rows, and every one of them was being carried out of the database,
// decoded into a struct and then thrown away to draw sixty points. Grouping in
// SQL means the database hands back the sixty: a fortieth of the rows for the
// same graph, and the same arithmetic done where the rows already are.
//
// Buckets are cut on absolute time rather than on a count of rows, so a gap in
// the samples stays a gap. An hour the monitor was not running is an hour with
// no bucket at all, where averaging by index closes it up and draws a line
// straight over the outage as though it had been measured.
//
// The bucket's own timestamp is its start, computed from the epoch rather than
// taken from a row in it, which keeps the points evenly spaced whatever the
// samples inside them happened to land on.

func ServerMetrics(db *sql.DB, since time.Time, bucket time.Duration) ([]MetricPoint, error) {
	secs := bucketSeconds(bucket)
	return queryPoints(db, `SELECT CAST(strftime('%s', ts) / ? AS INTEGER) * ?, AVG(cpu), AVG(mem), AVG(disk)
		FROM metrics WHERE ts >= ? GROUP BY 1 ORDER BY 1`, secs, secs, since.UTC())
}

func AppMetrics(db *sql.DB, appID string, since time.Time, bucket time.Duration) ([]MetricPoint, error) {
	// The literal zero keeps one scanner for both windows. An application's own
	// disk use is not sampled a minute at a time — it is a walk of a directory
	// tree, not a counter — so there is nothing to average here.
	secs := bucketSeconds(bucket)
	return queryPoints(db, `SELECT CAST(strftime('%s', ts) / ? AS INTEGER) * ?, AVG(cpu), AVG(mem_mb), 0
		FROM app_metrics WHERE app_id = ? AND ts >= ? GROUP BY 1 ORDER BY 1`, secs, secs, appID, since.UTC())
}

// bucketSeconds is the width of one bucket, in the unit the query divides by.
// A width under a second would divide by zero, and a caller asking for one is
// asking for every sample it has: a second is finer than anything is recorded
// at, so a one-second bucket holds exactly one row.
func bucketSeconds(bucket time.Duration) int64 {
	if secs := int64(bucket / time.Second); secs > 1 {
		return secs
	}
	return 1
}

func queryPoints(db *sql.DB, query string, args ...any) ([]MetricPoint, error) {
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MetricPoint
	for rows.Next() {
		var (
			p     MetricPoint
			epoch int64
		)
		if err := rows.Scan(&epoch, &p.V1, &p.V2, &p.V3); err != nil {
			return nil, err
		}
		p.TS = time.Unix(epoch, 0).UTC()
		out = append(out, p)
	}
	return out, rows.Err()
}

// PruneTimeSeries drops samples older than the retention window, reporting the
// first table it could not trim: one locked database fails all four the same
// way, and four copies of that in the log say nothing the first did not.
//
// A station's own series are not here. They are not dropped when they age out,
// they are folded into hours first, and that is FoldStationSeries' job.
func PruneTimeSeries(db *sql.DB, olderThan time.Time) error {
	cut := olderThan.UTC()
	return firstError(
		exec(db, "DELETE FROM metrics WHERE ts < ?", cut),
		exec(db, "DELETE FROM app_metrics WHERE ts < ?", cut),
		exec(db, "DELETE FROM health_history WHERE ts < ?", cut),
		exec(db, "DELETE FROM app_logs WHERE ts < ?", cut),
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
