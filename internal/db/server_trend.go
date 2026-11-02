package db

import (
	"database/sql"
	"time"
)

// ServerAverage is what the server averaged over a stretch of time, in the
// units its cards read a change in.
type ServerAverage struct {
	CPU           float64 // percent
	MemGB, DiskGB float64 // used
	// HasGB is false over a stretch sampled before the gigabytes were
	// recorded, which leaves nothing to compare a memory or disk figure with.
	HasGB bool
}

// ServerAverageBetween averages the samples, and the hours they were folded
// into, between two moments. ok is false when there are none.
//
// The gigabyte columns are averaged over the rows that carry them only: a row
// written before they were recorded holds a zero, and averaging that in would
// read as the disk having emptied.
func ServerAverageBetween(db *sql.DB, from, to time.Time) (avg ServerAverage, ok bool) {
	var (
		cpu, mem, disk sql.NullFloat64
	)
	f, t := from.UTC(), to.UTC()
	err := db.QueryRow(`SELECT SUM(cpu * n) / SUM(n),
			SUM(CASE WHEN mem_gb > 0 THEN mem_gb * n END) / SUM(CASE WHEN mem_gb > 0 THEN n END),
			SUM(CASE WHEN disk_gb > 0 THEN disk_gb * n END) / SUM(CASE WHEN disk_gb > 0 THEN n END)
		FROM (
			SELECT cpu, mem_gb, disk_gb, 1 AS n FROM metrics WHERE ts >= ? AND ts < ?
			UNION ALL
			SELECT cpu, mem_gb, disk_gb, samples FROM metrics_hourly WHERE hour >= ? AND hour < ?
		)`, f, t, f, t).Scan(&cpu, &mem, &disk)
	if err != nil || !cpu.Valid {
		return ServerAverage{}, false
	}
	return ServerAverage{CPU: cpu.Float64, MemGB: mem.Float64, DiskGB: disk.Float64,
		HasGB: mem.Valid && disk.Valid}, true
}

// DiskUsedGB is the disk's used gigabytes since a moment, one point an hour,
// for working out which way it is heading.
func DiskUsedGB(db *sql.DB, since time.Time) ([]MetricPoint, error) {
	from := since.UTC()
	rows, err := db.Query(`SELECT CAST(strftime('%s', ts) / 3600 AS INTEGER) * 3600, SUM(disk_gb * n) / SUM(n)
		FROM (
			SELECT ts, disk_gb, 1 AS n FROM metrics WHERE ts >= ? AND disk_gb > 0
			UNION ALL
			SELECT hour, disk_gb, samples FROM metrics_hourly WHERE hour >= ? AND disk_gb > 0
		) GROUP BY 1 ORDER BY 1`, from, from)
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
		if err := rows.Scan(&epoch, &p.V1); err != nil {
			return nil, err
		}
		p.TS = time.Unix(epoch, 0).UTC()
		out = append(out, p)
	}
	return out, rows.Err()
}
