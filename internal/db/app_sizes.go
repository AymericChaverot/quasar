package db

import (
	"database/sql"
	"time"
)

// --- Application sizes --------------------------------------------------------
//
// One row per application per sample: what its data directory weighed, and
// when. Everything here is about not walking that directory more often than it
// has to be walked.

// AppSizeSample is the newest measurement of one application's directory.
type AppSizeSample struct {
	Bytes int64
	TS    time.Time
}

func RecordAppSize(db *sql.DB, appID string, bytes int64) error {
	_, err := db.Exec("INSERT INTO app_sizes (app_id, bytes) VALUES (?, ?)", appID, bytes)
	return err
}

// LatestAppSizes is the newest sample for every application that has one.
//
// One query rather than one per application: the page asking is drawing a
// table of all of them, and the sampler asking is deciding which ones are due.
func LatestAppSizes(db *sql.DB) (map[string]AppSizeSample, error) {
	rows, err := db.Query(`SELECT app_id, bytes, ts FROM app_sizes
		WHERE (app_id, ts) IN (SELECT app_id, MAX(ts) FROM app_sizes GROUP BY app_id)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]AppSizeSample{}
	for rows.Next() {
		var (
			id string
			s  AppSizeSample
		)
		if err := rows.Scan(&id, &s.Bytes, &s.TS); err != nil {
			return nil, err
		}
		// Written by SQLite's own CURRENT_TIMESTAMP, which is UTC; saying so
		// here is what lets a caller compare it against time.Now().
		s.TS = s.TS.UTC()
		out[id] = s
	}
	return out, rows.Err()
}

// AppSizes reads one application's history, bucketed the way the other series
// are and in megabytes, which is the unit every figure about an application's
// storage is already given in.
//
// The bucket takes the largest sample in it rather than the average. A size is
// a level and not a rate: what somebody wants to see is the high-water mark of
// each hour, not a mean that hides the import that filled the disk and was
// tidied up again before the next sample.
func AppSizes(db *sql.DB, appID string, since time.Time, bucket time.Duration) ([]MetricPoint, error) {
	secs := bucketSeconds(bucket)
	rows, err := db.Query(`SELECT CAST(strftime('%s', ts) / ? AS INTEGER) * ?, MAX(bytes)
		FROM app_sizes WHERE app_id = ? AND ts >= ? GROUP BY 1 ORDER BY 1`,
		secs, secs, appID, since.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MetricPoint
	for rows.Next() {
		var (
			epoch int64
			bytes int64
		)
		if err := rows.Scan(&epoch, &bytes); err != nil {
			return nil, err
		}
		out = append(out, MetricPoint{TS: time.Unix(epoch, 0).UTC(), V1: float64(bytes) / (1 << 20)})
	}
	return out, rows.Err()
}

// PruneAppSizes drops samples older than the window.
//
// Kept far longer than the minute-by-minute series, and it costs almost
// nothing to: one row an hour per application is a few thousand a year, where
// the metrics tables take fourteen hundred a day. The window is the point of
// the table — a disk fills over months, and a graph that cannot look back
// further than a week cannot show it happening.
func PruneAppSizes(db *sql.DB, olderThan time.Time) error {
	return exec(db, "DELETE FROM app_sizes WHERE ts < ?", olderThan.UTC())
}
