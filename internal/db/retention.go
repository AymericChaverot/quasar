package db

import (
	"database/sql"
	"fmt"
	"strconv"
	"time"
)

// How long stored log lines and audit entries are kept, as the operator set
// it on the Settings page, and the two ways of getting rid of them early.
//
// Logs have always gone after a week; the week is now a setting. The audit
// trail has only ever been bounded by count — MaxAuditEntries still holds —
// and can now also be bounded by age.

// Settings keys for the two retention windows, in whole days.
const (
	SettingLogRetentionDays   = "log_retention_days"
	SettingAuditRetentionDays = "audit_retention_days"
)

// The bounds a retention window is held to. A log kept for no time at all
// would make the Logs page useless, so a day is the least; the audit trail
// may be kept with no age limit at all, which is 0.
const (
	DefaultLogRetentionDays = 7
	MinLogRetentionDays     = 1
	MaxLogRetentionDays     = 365

	DefaultAuditRetentionDays = 0 // no limit by age; MaxAuditEntries still applies
	MaxAuditRetentionDays     = 3650
)

// LogRetentionDays is how many days of log lines are kept.
func LogRetentionDays(db *sql.DB) int {
	return retentionDays(db, SettingLogRetentionDays, DefaultLogRetentionDays, MinLogRetentionDays, MaxLogRetentionDays)
}

// AuditRetentionDays is how many days of audit entries are kept, 0 for as
// long as MaxAuditEntries allows.
func AuditRetentionDays(db *sql.DB) int {
	return retentionDays(db, SettingAuditRetentionDays, DefaultAuditRetentionDays, 0, MaxAuditRetentionDays)
}

// retentionDays reads a stored window, falling back to the default for one
// never stored or one out of bounds — a hand-edited row must not become "keep
// nothing".
func retentionDays(db *sql.DB, key string, def, lo, hi int) int {
	n, err := strconv.Atoi(GetSetting(db, key))
	if err != nil || n < lo || n > hi {
		return def
	}
	return n
}

// PruneLogsOlderThan drops the log lines written before cut.
func PruneLogsOlderThan(db *sql.DB, cut time.Time) (int64, error) {
	return affected(db.Exec("DELETE FROM app_logs WHERE ts < ?", cut.UTC()))
}

// PruneAuditOlderThan drops the audit entries recorded before cut.
func PruneAuditOlderThan(db *sql.DB, cut time.Time) (int64, error) {
	return affected(db.Exec("DELETE FROM audit_log WHERE ts < ?", cut.UTC()))
}

// PurgeLogs deletes every stored log line, of every application.
func PurgeLogs(db *sql.DB) (int64, error) {
	return affected(db.Exec("DELETE FROM app_logs"))
}

// PurgeAudit deletes the whole audit trail and leaves one entry in its place,
// the purge itself, in the same transaction: a trail that can be emptied
// without a trace is not a trail. The entry is the caller's, so it names who
// did it and from where; its detail, when left empty, says how many entries
// went.
func PurgeAudit(db *sql.DB, purge AuditEntry) (int64, error) {
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck // the commit below is what matters; a rollback after it is a no-op
	n, err := affected(tx.Exec("DELETE FROM audit_log"))
	if err != nil {
		return 0, err
	}
	if purge.Detail == "" {
		purge.Detail = fmt.Sprintf("%d earlier entries deleted", n)
	}
	if _, err := tx.Exec(`INSERT INTO audit_log (actor, action, target, detail, ip) VALUES (?, ?, ?, ?, ?)`,
		purge.Actor, purge.Action, purge.Target, purge.Detail, purge.IP); err != nil {
		return 0, err
	}
	return n, tx.Commit()
}

func affected(res sql.Result, err error) (int64, error) {
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// PruneRetained drops the log lines and audit entries older than their
// windows allow, as of now.
func PruneRetained(db *sql.DB, now time.Time) error {
	_, logErr := PruneLogsOlderThan(db, now.AddDate(0, 0, -LogRetentionDays(db)))
	var auditErr error
	if days := AuditRetentionDays(db); days > 0 {
		_, auditErr = PruneAuditOlderThan(db, now.AddDate(0, 0, -days))
	}
	return firstError(logErr, auditErr)
}
