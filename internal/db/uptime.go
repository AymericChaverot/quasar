package db

import (
	"database/sql"
	"time"
)

// An application's uptime is its health probes, counted by day. The probes
// themselves are kept a week, as they always were, and are then folded into
// one row a day of how many there were and how many answered: ninety rows an
// application for three months of history, where the probes would be a
// quarter of a million.

// UptimeDay is one day of an application's probes, in UTC.
type UptimeDay struct {
	Day       time.Time
	OK, Total int
}

// Percent is the share of the day's probes that answered, or -1 for a day with
// none, which is not a day the application was down but one nobody asked.
func (d UptimeDay) Percent() float64 {
	if d.Total == 0 {
		return -1
	}
	return float64(d.OK) / float64(d.Total) * 100
}

// FoldHealth counts the probes older than foldBefore into their days, drops
// them, and drops the days older than dropBefore. A day the probes are folded
// from in two goes, either side of foldBefore, adds up rather than overwrites.
func FoldHealth(db *sql.DB, foldBefore, dropBefore time.Time) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // the commit below is what matters; a rollback after it is a no-op

	cut := foldBefore.UTC()
	for _, st := range []struct {
		query string
		arg   any
	}{
		{`INSERT INTO health_daily (app_id, day, ok, total)
		SELECT app_id, date(ts), SUM(ok), COUNT(*) FROM health_history WHERE ts < ? GROUP BY 1, 2
		ON CONFLICT (app_id, day) DO UPDATE SET ok = ok + excluded.ok, total = total + excluded.total`, cut},
		{`DELETE FROM health_history WHERE ts < ?`, cut},
		{`DELETE FROM health_daily WHERE day < ?`, dropBefore.UTC().Format(time.DateOnly)},
		// An incident is kept as long as the days it happened on.
		{`DELETE FROM incidents WHERE ended_at < ?`, dropBefore.UTC()},
	} {
		if _, err := tx.Exec(st.query, st.arg); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// UptimeDays is an application's last days of probes, oldest first, one entry
// a day whether or not it was probed, ending today.
func UptimeDays(db *sql.DB, appID string, days int, now time.Time) ([]UptimeDay, error) {
	today := now.UTC().Truncate(24 * time.Hour)
	first := today.AddDate(0, 0, -(days - 1))
	rows, err := db.Query(`SELECT day, SUM(ok), SUM(total) FROM (
			SELECT day, ok, total FROM health_daily WHERE app_id = ? AND day >= ?
			UNION ALL
			SELECT date(ts), SUM(ok), COUNT(*) FROM health_history WHERE app_id = ? AND ts >= ? GROUP BY 1
		) GROUP BY day`, appID, first.Format(time.DateOnly), appID, first)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counted := map[string]UptimeDay{}
	for rows.Next() {
		var day string
		var d UptimeDay
		if err := rows.Scan(&day, &d.OK, &d.Total); err != nil {
			return nil, err
		}
		counted[day] = d
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]UptimeDay, days)
	for i := range out {
		day := first.AddDate(0, 0, i)
		d := counted[day.Format(time.DateOnly)]
		d.Day = day
		out[i] = d
	}
	return out, nil
}
