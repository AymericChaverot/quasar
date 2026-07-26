package db

import (
	"database/sql"
	"time"
)

// AuditEntry is one recorded action. Entries are append-only and never
// rewritten: an audit trail that can be edited from the same UI it records is
// not worth keeping.
type AuditEntry struct {
	ID     int64
	TS     time.Time
	Actor  string // username, or "webhook"/"system" for unattended actions
	Action string // dotted verb, e.g. "app.delete", "master-key.download"
	Target string // what it acted on: app name, backup archive, username
	Detail string
	IP     string
}

// Audit actors that are not a logged-in person.
const (
	ActorSystem  = "system"
	ActorWebhook = "webhook"
)

// MaxAuditEntries caps the trail. It is a security record rather than a metric,
// so it is kept far longer than logs — but still bounded, because this database
// shares a small disk with everything else.
const MaxAuditEntries = 20_000

// RecordAudit appends an entry. Failures are swallowed: an audit write must
// never be the reason an operation the user asked for fails.
func RecordAudit(db *sql.DB, e AuditEntry) {
	db.Exec(`INSERT INTO audit_log (actor, action, target, detail, ip) VALUES (?, ?, ?, ?, ?)`,
		e.Actor, e.Action, e.Target, e.Detail, e.IP)
}

// ListAudit returns entries newest first, optionally filtered by a substring
// match across actor, action and target.
func ListAudit(db *sql.DB, query string, limit int) ([]*AuditEntry, error) {
	rows, err := db.Query(`
		SELECT id, ts, actor, action, target, detail, ip
		FROM audit_log
		WHERE ? = ''
		   OR actor LIKE '%' || ? || '%'
		   OR action LIKE '%' || ? || '%'
		   OR target LIKE '%' || ? || '%'
		ORDER BY id DESC
		LIMIT ?`, query, query, query, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*AuditEntry
	for rows.Next() {
		var e AuditEntry
		if err := rows.Scan(&e.ID, &e.TS, &e.Actor, &e.Action, &e.Target, &e.Detail, &e.IP); err != nil {
			return nil, err
		}
		out = append(out, &e)
	}
	return out, rows.Err()
}

// PruneAudit drops the oldest entries beyond MaxAuditEntries.
func PruneAudit(db *sql.DB) {
	db.Exec(`
		DELETE FROM audit_log WHERE id IN (
			SELECT id FROM audit_log ORDER BY id DESC LIMIT -1 OFFSET ?
		)`, MaxAuditEntries)
}
