package db

import (
	"database/sql"
	"time"
)

// The public status: which applications it names and under what name, what
// it shows of them, and the incidents it reports. Nothing is published until
// somebody chooses it: no application is listed by default, and the API is
// off.

// Status settings, kept with the platform's others.
const (
	// SettingStatusMode is who may read the status API: "off", "token" (any
	// API token) or "public".
	SettingStatusMode = "status_mode"
	// SettingStatusCORS is "true" to let other sites' pages read it.
	SettingStatusCORS = "status_cors"
	// The sections the API includes, each "true" or "false".
	SettingStatusShowState     = "status_show_state"
	SettingStatusShowUptime    = "status_show_uptime"
	SettingStatusShowIncidents = "status_show_incidents"
	SettingStatusShowServer    = "status_show_server"
	// How the status presents itself.
	SettingStatusTitle       = "status_title"
	SettingStatusDescription = "status_description"
	SettingStatusLink        = "status_link"
	// SettingStatusPage is "true" while the status page is meant to be
	// deployed, on SettingStatusSubdomain.
	SettingStatusPage      = "status_page"
	SettingStatusSubdomain = "status_subdomain"
	// SettingStatusPageKey is the secret the status page reads the API with,
	// whatever the API's mode: the page is how the operator chose to publish
	// the status, so turning the API off for everybody else leaves it working.
	SettingStatusPageKey = "status_page_key"
)

// StatusApp is an application the status names, and the name it goes by
// there: the one somebody outside knows it as, rather than what it is called
// on this server.
type StatusApp struct {
	AppID, Name string
}

// StatusApps is the published applications, keyed by id.
func StatusApps(db *sql.DB) (map[string]string, error) {
	rows, err := db.Query(`SELECT app_id, name FROM status_apps`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var a StatusApp
		if err := rows.Scan(&a.AppID, &a.Name); err != nil {
			return nil, err
		}
		out[a.AppID] = a.Name
	}
	return out, rows.Err()
}

// SetStatusApps replaces the published applications with these.
func SetStatusApps(db *sql.DB, apps []StatusApp) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // the commit below is what matters; a rollback after it is a no-op
	if _, err := tx.Exec(`DELETE FROM status_apps`); err != nil {
		return err
	}
	for _, a := range apps {
		if _, err := tx.Exec(`INSERT INTO status_apps (app_id, name) VALUES (?, ?)`, a.AppID, a.Name); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Incident is a stretch of time an application did not answer its health
// check: from the first check that failed to the next one that answered, or
// still open. Restarted is whether Quasar restarted it during the stretch.
type Incident struct {
	AppID     string
	Started   time.Time
	Ended     *time.Time
	Restarted bool
}

// OpenIncident records that an application is down, unless it already is. The
// incident starts at the first of the checks that have failed since the last
// one that answered: the moment it stopped answering, not the moment that
// was decided.
func OpenIncident(db *sql.DB, appID string) error {
	_, err := db.Exec(`INSERT INTO incidents (app_id, started_at)
		SELECT ?, COALESCE((SELECT MIN(ts) FROM health_history WHERE app_id = ? AND ok = 0
			AND ts > COALESCE((SELECT MAX(ts) FROM health_history WHERE app_id = ? AND ok = 1), 0)), CURRENT_TIMESTAMP)
		WHERE NOT EXISTS (SELECT 1 FROM incidents WHERE app_id = ? AND ended_at IS NULL)`,
		appID, appID, appID, appID)
	return err
}

// MarkIncidentRestarted notes that the application's open incident led to a
// restart.
func MarkIncidentRestarted(db *sql.DB, appID string) error {
	return exec(db, `UPDATE incidents SET restarted = 1 WHERE app_id = ? AND ended_at IS NULL`, appID)
}

// CloseIncident ends the application's open incident, if it has one.
func CloseIncident(db *sql.DB, appID string) error {
	return exec(db, `UPDATE incidents SET ended_at = CURRENT_TIMESTAMP WHERE app_id = ? AND ended_at IS NULL`, appID)
}

// Incidents is every incident that was open at some point since the given
// time, newest first.
func Incidents(db *sql.DB, since time.Time) ([]Incident, error) {
	rows, err := db.Query(`SELECT app_id, CAST(strftime('%s', started_at) AS INTEGER),
			CAST(strftime('%s', ended_at) AS INTEGER), restarted
		FROM incidents WHERE ended_at IS NULL OR ended_at >= ?
		ORDER BY started_at DESC, id DESC`, since.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Incident
	for rows.Next() {
		var in Incident
		var started int64
		var ended sql.NullInt64
		if err := rows.Scan(&in.AppID, &started, &ended, &in.Restarted); err != nil {
			return nil, err
		}
		in.Started = time.Unix(started, 0).UTC()
		if ended.Valid {
			t := time.Unix(ended.Int64, 0).UTC()
			in.Ended = &t
		}
		out = append(out, in)
	}
	return out, rows.Err()
}
