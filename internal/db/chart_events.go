package db

import (
	"database/sql"
	"time"
)

// Kinds of ChartEvent.
const (
	EventDeploy  = "deploy"
	EventRestart = "restart"
	EventAlert   = "alert"
	EventRecover = "recover"
)

// ChartEvent is one moment a chart of the server marks, with what it says.
type ChartEvent struct {
	At    time.Time
	Kind  string
	Label string
}

// maxChartEvents is as many as a chart marks. A webhook that deploys on every
// push can make hundreds in a month, and a chart ruled with hundreds of lines
// is a chart nobody can read; the newest are the ones worth seeing.
const maxChartEvents = 40

// RecordChartEvent stores one moment for the charts. appID is empty for one
// that belongs to the server rather than to an application.
func RecordChartEvent(db *sql.DB, kind, appID, label string) error {
	_, err := db.Exec("INSERT INTO chart_events (kind, app_id, label) VALUES (?, ?, ?)", kind, appID, label)
	return err
}

// ChartEvents is what happened since a moment, oldest first: the events
// recorded for the charts, and every deployment started, named after its
// application.
func ChartEvents(db *sql.DB, since time.Time) ([]ChartEvent, error) {
	from := since.UTC()
	rows, err := db.Query(`SELECT at, kind, label FROM (
			SELECT ts AS at, rowid AS seq, kind, label FROM chart_events WHERE ts >= ?
			UNION ALL
			SELECT d.started_at, d.id, ?, 'Deploy · ' || COALESCE(a.name, d.app_id)
			FROM deployments d LEFT JOIN apps a ON a.id = d.app_id
			WHERE d.started_at >= ?
		) ORDER BY at DESC, seq DESC LIMIT ?`, from, EventDeploy, from, maxChartEvents)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ChartEvent
	for rows.Next() {
		var e ChartEvent
		if err := rows.Scan(&e.At, &e.Kind, &e.Label); err != nil {
			return nil, err
		}
		e.At = e.At.UTC()
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Two in the same second keep the order they were written in. Read newest first so the cap keeps the newest; handed back in the order
	// a chart draws them.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}
