package db

import (
	"database/sql"
	"time"
)

// An operator's own catalogue, stored as the YAML document it was written or
// imported as rather than as parsed-out columns.
//
// The document is the unit on purpose. A catalogue is a thing people write
// once, share, and re-import when it changes — so keeping the text lets it be
// exported back out byte for byte, re-fetched from where it came from and
// diffed against what is stored, none of which survives being shredded into
// rows. Parsing happens in the catalog package, which is where the format
// lives; this table holds text and knows nothing about entries.
type Catalog struct {
	ID   int64
	Name string

	// SourceURL is where the document was fetched from, empty for one written
	// here. It is only a record of where to look again: nothing re-fetches on
	// its own, because a catalogue changing under an operator would change what
	// their next deploy runs.
	SourceURL string

	YAML      string
	Enabled   bool
	Position  int
	UpdatedAt time.Time
}

// ListCatalogs returns every stored catalogue in merge order.
func ListCatalogs(db *sql.DB) ([]*Catalog, error) {
	rows, err := db.Query(`SELECT id, name, source_url, yaml, enabled, position, updated_at
		FROM catalogs ORDER BY position, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Catalog
	for rows.Next() {
		var c Catalog
		if err := rows.Scan(&c.ID, &c.Name, &c.SourceURL, &c.YAML, &c.Enabled, &c.Position, &c.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, &c)
	}
	return out, rows.Err()
}

func GetCatalog(db *sql.DB, id int64) *Catalog {
	var c Catalog
	err := db.QueryRow(`SELECT id, name, source_url, yaml, enabled, position, updated_at
		FROM catalogs WHERE id = ?`, id).
		Scan(&c.ID, &c.Name, &c.SourceURL, &c.YAML, &c.Enabled, &c.Position, &c.UpdatedAt)
	if err != nil {
		return nil
	}
	return &c
}

// InsertCatalog stores a new catalogue at the end of the merge order and
// returns its ID.
func InsertCatalog(db *sql.DB, c *Catalog) (int64, error) {
	var last int
	db.QueryRow("SELECT COALESCE(MAX(position), 0) FROM catalogs").Scan(&last)
	res, err := db.Exec(`INSERT INTO catalogs (name, source_url, yaml, enabled, position, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`, c.Name, c.SourceURL, c.YAML, c.Enabled, last+1, time.Now())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func UpdateCatalog(db *sql.DB, c *Catalog) error {
	_, err := db.Exec(`UPDATE catalogs SET name = ?, source_url = ?, yaml = ?, enabled = ?, updated_at = ?
		WHERE id = ?`, c.Name, c.SourceURL, c.YAML, c.Enabled, time.Now(), c.ID)
	return err
}

func SetCatalogEnabled(db *sql.DB, id int64, enabled bool) error {
	_, err := db.Exec("UPDATE catalogs SET enabled = ? WHERE id = ?", enabled, id)
	return err
}

func DeleteCatalog(db *sql.DB, id int64) error {
	_, err := db.Exec("DELETE FROM catalogs WHERE id = ?", id)
	return err
}
