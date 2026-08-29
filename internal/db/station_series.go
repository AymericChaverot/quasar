package db

import (
	"database/sql"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"
	"time"
)

// quasar.series: what a station measured about its own application, kept over
// time.
//
// It is the store's opposite number and exists because the store cannot do
// this. A store holds what an action worked out and wants back next time, in
// 256 KB, under a key it overwrites; a series holds the same number sampled on
// Tuesday and again on Wednesday, which is what a chart is drawn from. A
// station could in principle keep its own history in the store — read the
// list, append, write it back — and it would fill the cap in a few days, lose
// the lot on one failed write, and re-marshal the whole thing on every sample.
//
// Like the store it needs no permission: it is scoped to one application and
// one station and can reach nothing else. Unlike the store it is bounded in
// two directions rather than one, because a table that only ever grows is a
// disk somebody else fills.

// MaxSeriesNames is how many distinct series one station may keep for one
// application. A station charts a handful of things about one service —
// players, ticks, queue depth — and a document that wants a hundred is either
// doing something the metrics tables already do or writing a database.
//
// The refusal names the series it would not create rather than silently
// dropping the sample, because a chart that is quietly always empty is a bug
// the author finds last.
const MaxSeriesNames = 8

// seriesName is what a series may be called: the shape of an identifier, so
// that a name reads the same in the document that charts it as in the script
// that records it.
var seriesName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

// SeriesPoint is one sample of one series.
type SeriesPoint struct {
	TS    time.Time
	Value float64
}

// RecordStationSeries appends one sample.
//
// Every check a series has is here rather than at the caller, because this is
// the privileged side of the worker boundary and a script is what is on the
// other one.
func RecordStationSeries(db *sql.DB, appID, stationID, name string, value float64) error {
	if !seriesName.MatchString(name) {
		return fmt.Errorf("%q is not a series name: lowercase letters, digits and underscores, starting with a letter", name)
	}
	// SQLite stores a NaN as NULL and an infinity as a number nothing can
	// chart. A station dividing by zero should read that back rather than find
	// a gap it cannot explain.
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return fmt.Errorf("%s: %v is not a value a series can hold", name, value)
	}

	names, err := StationSeriesNames(db, appID, stationID)
	if err != nil {
		return err
	}
	if len(names) >= MaxSeriesNames && !slices.Contains(names, name) {
		return fmt.Errorf("this station already keeps %d series for this application (%s), which is the most it may",
			len(names), strings.Join(names, ", "))
	}

	// The column's own default writes the time, which is what every other
	// sampled table here does. A timestamp written from Go and one written by
	// SQLite are not the same text, and a window that compares the two reads
	// as an empty series rather than as a bug.
	_, err = db.Exec(`INSERT INTO station_series (app_id, station_id, name, value)
		VALUES (?, ?, ?, ?)`, appID, stationID, name, value)
	return err
}

// StationSeries reads one series back, oldest first, which is the order a
// chart draws it in.
//
// It reads both halves of the series and lets the data say where the seam is.
// Recent hours are still samples; older ones have been folded into one row an
// hour and come back as those hours' means. There is no overlap to resolve
// because folding deletes what it folded, and no window to hard-code because
// nothing here needs to know where the fold has got to — which is what keeps
// this correct if the retention is ever changed.
func StationSeries(db *sql.DB, appID, stationID, name string, since time.Time) ([]SeriesPoint, error) {
	cut := since.UTC()
	rows, err := db.Query(`
		SELECT hour AS ts, avg_value AS value FROM station_series_hourly
			WHERE app_id = ? AND station_id = ? AND name = ? AND hour >= ?
		UNION ALL
		SELECT ts, value FROM station_series
			WHERE app_id = ? AND station_id = ? AND name = ? AND ts >= ?
		ORDER BY ts`,
		appID, stationID, name, cut, appID, stationID, name, cut)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SeriesPoint{}
	for rows.Next() {
		var p SeriesPoint
		if err := rows.Scan(&p.TS, &p.Value); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// HourlyPoint is one folded hour: what the series averaged over it, and the
// least and the most it reached, which is what an average on its own throws
// away.
type HourlyPoint struct {
	Hour     time.Time
	Avg      float64
	Min, Max float64
	Samples  int
}

// StationSeriesHourly reads the folded half of a series, oldest first.
func StationSeriesHourly(db *sql.DB, appID, stationID, name string, since time.Time) ([]HourlyPoint, error) {
	rows, err := db.Query(`SELECT hour, avg_value, min_value, max_value, samples
		FROM station_series_hourly
		WHERE app_id = ? AND station_id = ? AND name = ? AND hour >= ? ORDER BY hour`,
		appID, stationID, name, since.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []HourlyPoint{}
	for rows.Next() {
		var p HourlyPoint
		if err := rows.Scan(&p.Hour, &p.Avg, &p.Min, &p.Max, &p.Samples); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// FoldStationSeries turns every sample older than foldBefore into one row an
// hour, and drops folded hours older than dropBefore.
//
// The two happen together, in one transaction, because a fold that inserted
// and then failed to delete would double the samples it had just counted the
// next time it ran. Doing both in one statement pair and one commit means a
// worker killed in the middle leaves the series exactly as it found them.
//
// An hour that somehow arrives twice is merged rather than replaced: the mean
// is re-weighted by how many samples each side counted, and the least and the
// most are the least and the most of both. Nothing should produce that — the
// fold deletes what it folded, and no sample is ever written with a time in
// the past — but a merge that composes is one fewer way for a graph to be
// quietly wrong.
func FoldStationSeries(db *sql.DB, foldBefore, dropBefore time.Time) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // the commit below is what matters; a rollback after it is a no-op

	cut := foldBefore.UTC()
	if _, err := tx.Exec(`
		INSERT INTO station_series_hourly
			(app_id, station_id, name, hour, avg_value, min_value, max_value, samples)
		SELECT app_id, station_id, name, strftime('%Y-%m-%d %H:00:00', ts),
			AVG(value), MIN(value), MAX(value), COUNT(*)
		FROM station_series WHERE ts < ?
		GROUP BY app_id, station_id, name, strftime('%Y-%m-%d %H:00:00', ts)
		ON CONFLICT (app_id, station_id, name, hour) DO UPDATE SET
			avg_value = (avg_value * samples + excluded.avg_value * excluded.samples)
				/ (samples + excluded.samples),
			min_value = MIN(min_value, excluded.min_value),
			max_value = MAX(max_value, excluded.max_value),
			samples   = samples + excluded.samples`, cut); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM station_series WHERE ts < ?`, cut); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM station_series_hourly WHERE hour < ?`, dropBefore.UTC()); err != nil {
		return err
	}
	return tx.Commit()
}

// StationSeriesNames lists what a station is keeping for one application, in
// order, so that the cap counts the same thing twice running.
//
// Both halves, because a series that has been running long enough to be folded
// is still one of the eight this station is keeping.
func StationSeriesNames(db *sql.DB, appID, stationID string) ([]string, error) {
	rows, err := db.Query(`SELECT DISTINCT name FROM (
			SELECT name FROM station_series WHERE app_id = ? AND station_id = ?
			UNION ALL
			SELECT name FROM station_series_hourly WHERE app_id = ? AND station_id = ?
		) ORDER BY name`, appID, stationID, appID, stationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

// A series is scoped to a pair, like the store, and is orphaned the same two
// ways: the application it measured can go, and so can the station measuring
// it.

// DeleteAppSeries clears everything every station measured about one
// application.
func DeleteAppSeries(db *sql.DB, appID string) error {
	return firstError(
		exec(db, `DELETE FROM station_series WHERE app_id = ?`, appID),
		exec(db, `DELETE FROM station_series_hourly WHERE app_id = ?`, appID),
	)
}

// DeleteStationSeries clears everything one station measured, on every
// application it ran on.
func DeleteStationSeries(db *sql.DB, stationID string) error {
	return firstError(
		exec(db, `DELETE FROM station_series WHERE station_id = ?`, stationID),
		exec(db, `DELETE FROM station_series_hourly WHERE station_id = ?`, stationID),
	)
}
