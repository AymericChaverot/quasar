package db

import (
	"database/sql"
	"time"
)

// A station, stored as the YAML document it was pasted or fetched as, for the
// reason a catalogue is: the document is the unit people write, share and
// re-import, and keeping the text is what lets it be exported back out byte
// for byte and diffed against what arrives next. Parsing happens in the
// station package, which is where the format lives; this table holds text.
//
// Three revision columns rather than a revisions table. Approved, previous and
// pending is the whole state machine — what is running, what to fall back to,
// and what has been fetched but not yet accepted — and a station with a hundred
// revisions is not a problem anybody has.
type Station struct {
	ID int64

	// StationID is the document's own id, unique across the installed
	// stations. Unlike a catalogue entry, a station does not override another
	// by reusing its id: a station is a program, and silently replacing
	// somebody's program with somebody else's is not a feature.
	StationID string

	Name string

	// SourceURL is where the document was fetched from, empty for one pasted
	// in. Nothing re-fetches on its own.
	SourceURL string

	// YAML is the approved revision: the one every application running this
	// station is served from.
	YAML string

	// PermsHash is what was accepted. A revision whose permissions hash to
	// something else is held until somebody accepts it.
	PermsHash string

	// PrevYAML is the revision before this one, so reverting a broken update
	// is one click and the application never stopped.
	PrevYAML string

	// PendingYAML is a fetched revision waiting to be approved, with the hash
	// it would be approved as.
	PendingYAML string
	PendingHash string

	Enabled   bool
	Position  int
	UpdatedAt time.Time
}

const stationColumns = `id, station_id, name, source_url, yaml, perms_hash,
	prev_yaml, pending_yaml, pending_hash, enabled, position, updated_at`

func scanStation(row interface{ Scan(...any) error }) (*Station, error) {
	var s Station
	err := row.Scan(&s.ID, &s.StationID, &s.Name, &s.SourceURL, &s.YAML, &s.PermsHash,
		&s.PrevYAML, &s.PendingYAML, &s.PendingHash, &s.Enabled, &s.Position, &s.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// ListStations returns every installed station, in the order the page shows
// them.
func ListStations(db *sql.DB) ([]*Station, error) {
	rows, err := db.Query(`SELECT ` + stationColumns + ` FROM stations ORDER BY position, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Station
	for rows.Next() {
		s, err := scanStation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func GetStation(db *sql.DB, id int64) *Station {
	s, err := scanStation(db.QueryRow(`SELECT `+stationColumns+` FROM stations WHERE id = ?`, id))
	if err != nil {
		return nil
	}
	return s
}

// GetStationByStationID finds a station by the id its own document declares,
// which is what an import checks before storing anything: the collision has to
// be reported naming the station already holding it.
func GetStationByStationID(db *sql.DB, stationID string) *Station {
	s, err := scanStation(db.QueryRow(`SELECT `+stationColumns+` FROM stations WHERE station_id = ?`, stationID))
	if err != nil {
		return nil
	}
	return s
}

// InsertStation stores a newly approved station at the end of the list.
func InsertStation(db *sql.DB, s *Station) (int64, error) {
	var last int
	db.QueryRow("SELECT COALESCE(MAX(position), 0) FROM stations").Scan(&last)
	res, err := db.Exec(`INSERT INTO stations
		(station_id, name, source_url, yaml, perms_hash, prev_yaml, pending_yaml, pending_hash, enabled, position, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.StationID, s.Name, s.SourceURL, s.YAML, s.PermsHash,
		s.PrevYAML, s.PendingYAML, s.PendingHash, s.Enabled, last+1, time.Now())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateStation writes back everything a revision can change. The station id
// is not among them: it is the identity, and a document that changes it is a
// different station.
func UpdateStation(db *sql.DB, s *Station) error {
	_, err := db.Exec(`UPDATE stations SET name = ?, source_url = ?, yaml = ?, perms_hash = ?,
		prev_yaml = ?, pending_yaml = ?, pending_hash = ?, enabled = ?, updated_at = ?
		WHERE id = ?`,
		s.Name, s.SourceURL, s.YAML, s.PermsHash,
		s.PrevYAML, s.PendingYAML, s.PendingHash, s.Enabled, time.Now(), s.ID)
	return err
}

// CountEnabledStations is what the header asks on every page: whether there is
// a Stations page worth linking to at all. An install with no station has no
// business carrying a navigation entry for one.
func CountEnabledStations(db *sql.DB) int {
	var n int
	db.QueryRow("SELECT COUNT(*) FROM stations WHERE enabled = 1").Scan(&n)
	return n
}

// CountAppsForStation is how many applications were deployed from a station,
// which is what makes removing one a decision rather than a click.
func CountAppsForStation(db *sql.DB, stationID string) int {
	var n int
	db.QueryRow("SELECT COUNT(*) FROM apps WHERE station_id = ?", stationID).Scan(&n)
	return n
}

func SetStationEnabled(db *sql.DB, id int64, enabled bool) error {
	_, err := db.Exec("UPDATE stations SET enabled = ? WHERE id = ?", enabled, id)
	return err
}

func DeleteStation(db *sql.DB, id int64) error {
	_, err := db.Exec("DELETE FROM stations WHERE id = ?", id)
	return err
}
