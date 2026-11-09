package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// The apps table checks deploy_type against a fixed list, and SQLite cannot
// change a CHECK constraint in place: the table has to be rebuilt with the new
// one. So a database created before "local" existed is rebuilt here, once,
// the way SQLite documents for any schema change ALTER TABLE cannot make — a
// new table from the old one's own definition, the rows copied, the names
// swapped — inside one transaction, so a failure leaves the old table as it
// was.

const (
	oldDeployTypes = "CHECK (deploy_type IN ('image', 'git', 'compose'))"
	newDeployTypes = "CHECK (deploy_type IN ('image', 'git', 'compose', 'local'))"
)

// allowLocalDeployType widens the apps table's deploy_type check to accept
// "local", and does nothing on a database whose table already does.
func allowLocalDeployType(db *sql.DB) error {
	var def string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'apps'`).Scan(&def); err != nil {
		return err
	}
	if !strings.Contains(def, oldDeployTypes) {
		return nil
	}
	// Built from the table's stored definition rather than from the schema
	// above: columns added since by ALTER TABLE are in it, in the order the
	// rows hold them, which is what lets the copy be a plain SELECT *.
	next := strings.Replace(def, oldDeployTypes, newDeployTypes, 1)
	next, ok := strings.CutPrefix(next, "CREATE TABLE apps")
	if !ok {
		return fmt.Errorf("unexpected definition of the apps table: %.40q", def)
	}
	next = "CREATE TABLE apps_next" + next

	ctx := context.Background()
	// One connection throughout: foreign_keys is per connection, and cannot be
	// changed inside a transaction. Nothing references apps today, but the
	// documented procedure turns it off all the same, so that a future
	// reference cannot turn the DROP below into a cascade.
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys = OFF"); err != nil {
		return err
	}
	defer func() { _, _ = conn.ExecContext(ctx, "PRAGMA foreign_keys = ON") }()

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, stmt := range []string{
		next,
		"INSERT INTO apps_next SELECT * FROM apps",
		"DROP TABLE apps",
		"ALTER TABLE apps_next RENAME TO apps",
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("%.40s: %w", stmt, err)
		}
	}
	return tx.Commit()
}
