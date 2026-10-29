package server

import (
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"quasar/internal/db"
	"quasar/internal/event"
)

// How long stored logs and the audit trail are kept, and emptying either at
// once. All three live on the Settings page, in its Logs & audit section.

// retentionData is what the Settings page shows of it.
func (s *Server) retentionData() map[string]any {
	logs, _ := db.CountLogs(s.db, "", "")
	audits, _ := db.CountAudit(s.db, "")
	return map[string]any{
		"LogDays":      db.LogRetentionDays(s.db),
		"MinLogDays":   db.MinLogRetentionDays,
		"MaxLogDays":   db.MaxLogRetentionDays,
		"AuditDays":    db.AuditRetentionDays(s.db),
		"MaxAuditDays": db.MaxAuditRetentionDays,
		"MaxAudit":     db.MaxAuditEntries,
		"LogLines":     logs,
		"AuditCount":   audits,
	}
}

// handleRetention stores the two windows and applies them at once, rather
// than at the monitor's next hourly sweep: somebody who has just shortened a
// window expects what falls outside it to be gone when the page comes back.
func (s *Server) handleRetention(w http.ResponseWriter, r *http.Request) {
	logDays, err1 := strconv.Atoi(strings.TrimSpace(r.FormValue("log_days")))
	auditDays, err2 := strconv.Atoi(strings.TrimSpace(r.FormValue("audit_days")))
	if err1 != nil || err2 != nil ||
		logDays < db.MinLogRetentionDays || logDays > db.MaxLogRetentionDays ||
		auditDays < 0 || auditDays > db.MaxAuditRetentionDays {
		s.redirectWith(w, r, "/settings#retention", warnNotice("Retention not saved", fmt.Sprintf(
			"Logs are kept %d to %d days; the audit trail 0 (no limit) to %d.",
			db.MinLogRetentionDays, db.MaxLogRetentionDays, db.MaxAuditRetentionDays)))
		return
	}
	for key, v := range map[string]int{db.SettingLogRetentionDays: logDays, db.SettingAuditRetentionDays: auditDays} {
		if err := db.SetSetting(s.db, key, strconv.Itoa(v)); err != nil {
			s.redirectWith(w, r, "/settings#retention", errNotice("Retention not saved", "", err))
			return
		}
	}
	if err := db.PruneRetained(s.db, time.Now()); err != nil {
		log.Printf("retention: applying the new windows: %v", err)
	}
	s.logCounts.forget()

	said := "logs " + days(logDays) + ", the audit trail " + auditWindow(auditDays)
	s.audit(r, "settings.retention", "", said)
	event.Info("retention", said, "by "+s.actor(r))
	s.redirectWith(w, r, "/settings#retention", okNotice("Retention saved", "Keeping "+said+"."))
}

// handlePurgeLogs deletes every stored log line. What the containers write
// from now on is stored as before, and a live log view is not affected.
func (s *Server) handlePurgeLogs(w http.ResponseWriter, r *http.Request) {
	n, err := db.PurgeLogs(s.db)
	if err != nil {
		s.redirectWith(w, r, "/settings#retention", errNotice("Logs not deleted", "", err))
		return
	}
	s.logCounts.forget()
	s.reclaimLater()
	said := count(n, "stored line", "stored lines") + " deleted"
	s.audit(r, "logs.purge", "", said)
	event.Info("retention", said, "by "+s.actor(r))
	s.redirectWith(w, r, "/settings#retention", okNotice("Logs deleted", said+"."))
}

// handlePurgeAudit empties the audit trail. PurgeAudit leaves the purge as
// the trail's first entry, who and from where, so it is not recorded again.
func (s *Server) handlePurgeAudit(w http.ResponseWriter, r *http.Request) {
	n, err := db.PurgeAudit(s.db, db.AuditEntry{Actor: s.actor(r), Action: "audit.purge", IP: clientIP(r)})
	if err != nil {
		s.redirectWith(w, r, "/settings#retention", errNotice("Audit trail not emptied", "", err))
		return
	}
	s.reclaimLater()
	said := count(n, "entry", "entries") + " deleted"
	event.Warning("retention", "audit trail emptied: "+said, "by "+s.actor(r))
	s.redirectWith(w, r, "/settings#retention", okNotice("Audit trail emptied", said+"; the purge itself is its first entry."))
}

// reclaimLater gives the disk back what a purge freed, off the request: the
// compaction rewrites the database and can take a while on a large one, and
// only runs when there is enough to be worth it.
func (s *Server) reclaimLater() {
	go func() {
		if freed := db.Reclaim(s.db); freed > 0 {
			event.Info("database", fmt.Sprintf("reclaimed %d MB of space", freed>>20))
		}
	}()
}

func days(n int) string { return count(int64(n), "day", "days") }

// auditWindow says how long the audit trail is kept, where 0 means no limit
// by age.
func auditWindow(n int) string {
	if n == 0 {
		return fmt.Sprintf("with no age limit (the newest %d entries)", db.MaxAuditEntries)
	}
	return days(n)
}

func count(n int64, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.FormatInt(n, 10) + " " + many
}
