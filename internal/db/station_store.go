package db

import (
	"database/sql"
	"fmt"
	"time"
)

// quasar.store: what a station is allowed to remember.
//
// It is scoped to one application and one station, which is what makes it need
// no permission — there is nothing in it a station could reach that is not its
// own. It exists at all because the process a script runs in holds no disk and
// dies at the end of every call, so anything a station wants to know next time
// has to have been handed to the parent.

// MaxStoreBytes is what one application's store may hold for one station,
// counted over the keys and the values together. It is a scratch space for
// what an action worked out — which mods have updates, when a check last ran —
// and not somewhere to keep a copy of the world.
const MaxStoreBytes = 256 << 10

// StationStoreGet returns a stored value, and whether there was one. The
// difference matters to a script: a key that was never set reads as undefined,
// and one set to null reads as null.
func StationStoreGet(db *sql.DB, appID, stationID, key string) (string, bool) {
	var value string
	err := db.QueryRow(`SELECT value FROM station_store
		WHERE app_id = ? AND station_id = ? AND key = ?`, appID, stationID, key).Scan(&value)
	if err != nil {
		return "", false
	}
	return value, true
}

// StationStoreSet writes one value, refusing the write that would take the
// store over its cap rather than silently dropping the oldest of anything: a
// station that has outgrown a scratch space should find that out.
func StationStoreSet(db *sql.DB, appID, stationID, key, value string) error {
	used, err := StationStoreBytes(db, appID, stationID)
	if err != nil {
		return err
	}
	if old, ok := StationStoreGet(db, appID, stationID, key); ok {
		used -= len(key) + len(old)
	}
	if used+len(key)+len(value) > MaxStoreBytes {
		return fmt.Errorf("this station's store for this application is full (%d KB); delete something first",
			MaxStoreBytes/1024)
	}

	_, err = db.Exec(`INSERT INTO station_store (app_id, station_id, key, value, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (app_id, station_id, key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		appID, stationID, key, value, time.Now())
	return err
}

func StationStoreDelete(db *sql.DB, appID, stationID, key string) error {
	_, err := db.Exec(`DELETE FROM station_store WHERE app_id = ? AND station_id = ? AND key = ?`,
		appID, stationID, key)
	return err
}

// StationStoreKeys lists what is in the store, in order, so a script iterating
// it twice sees the same thing twice.
func StationStoreKeys(db *sql.DB, appID, stationID string) ([]string, error) {
	rows, err := db.Query(`SELECT key FROM station_store
		WHERE app_id = ? AND station_id = ? ORDER BY key`, appID, stationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// StationStoreBytes is how much of the cap is used.
func StationStoreBytes(db *sql.DB, appID, stationID string) (int, error) {
	var n sql.NullInt64
	err := db.QueryRow(`SELECT SUM(LENGTH(key) + LENGTH(value)) FROM station_store
		WHERE app_id = ? AND station_id = ?`, appID, stationID).Scan(&n)
	if err != nil {
		return 0, err
	}
	return int(n.Int64), nil
}

// DeleteStationStore clears everything one station kept for one application.
// Called when the application goes, so a store does not outlive the thing it
// was about.
func DeleteStationStore(db *sql.DB, appID string) error {
	_, err := db.Exec(`DELETE FROM station_store WHERE app_id = ?`, appID)
	return err
}
