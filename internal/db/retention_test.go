package db

import (
	"strconv"
	"testing"
	"time"
)

// A window never stored, or stored out of bounds, is the default: a row edited
// by hand must not become "keep nothing".
func TestRetentionDaysFallsBackToTheDefault(t *testing.T) {
	d := openTestDB(t)
	if got := LogRetentionDays(d); got != DefaultLogRetentionDays {
		t.Errorf("unset log retention = %d", got)
	}
	if got := AuditRetentionDays(d); got != 0 {
		t.Errorf("unset audit retention = %d, want no limit", got)
	}
	for _, v := range []string{"0", "-3", "9999", "soon"} {
		if err := SetSetting(d, SettingLogRetentionDays, v); err != nil {
			t.Fatal(err)
		}
		if got := LogRetentionDays(d); got != DefaultLogRetentionDays {
			t.Errorf("log retention %q read as %d", v, got)
		}
	}
	if err := SetSetting(d, SettingLogRetentionDays, "30"); err != nil {
		t.Fatal(err)
	}
	if got := LogRetentionDays(d); got != 30 {
		t.Errorf("log retention 30 read as %d", got)
	}
}

// Lines and entries older than their windows go; newer ones stay; with no age
// limit on the audit trail, none of it goes by age.
func TestPruneRetainedKeepsTheWindow(t *testing.T) {
	d := openTestDB(t)
	now := time.Now().UTC()
	for _, age := range []int{1, 5, 10, 40} {
		ts := now.AddDate(0, 0, -age)
		if err := AppendLogs(d, "a1", []LogEntry{{TS: ts, Line: "line " + strconv.Itoa(age)}}); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Exec("INSERT INTO audit_log (ts, actor, action) VALUES (?, 'ada', 'x')", ts); err != nil {
			t.Fatal(err)
		}
	}
	count := func(table string) int {
		var n int
		if err := d.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// The defaults: a week of logs, the whole audit trail.
	if err := PruneRetained(d, now); err != nil {
		t.Fatal(err)
	}
	if count("app_logs") != 2 || count("audit_log") != 4 {
		t.Errorf("defaults kept %d lines and %d entries, want 2 and 4", count("app_logs"), count("audit_log"))
	}

	if err := SetSetting(d, SettingLogRetentionDays, "2"); err != nil {
		t.Fatal(err)
	}
	if err := SetSetting(d, SettingAuditRetentionDays, "30"); err != nil {
		t.Fatal(err)
	}
	if err := PruneRetained(d, now); err != nil {
		t.Fatal(err)
	}
	if count("app_logs") != 1 || count("audit_log") != 3 {
		t.Errorf("2 and 30 days kept %d lines and %d entries, want 1 and 3", count("app_logs"), count("audit_log"))
	}
}

// Purging the audit trail leaves the purge behind it, and nothing else.
func TestPurgeAuditLeavesItsOwnEntry(t *testing.T) {
	d := openTestDB(t)
	for i := range 5 {
		if err := RecordAudit(d, AuditEntry{Actor: "ada", Action: "app.deploy", Target: strconv.Itoa(i)}); err != nil {
			t.Fatal(err)
		}
	}
	n, err := PurgeAudit(d, AuditEntry{Actor: "ada", Action: "audit.purge", IP: "10.0.0.1"})
	if err != nil || n != 5 {
		t.Fatalf("purged %d (%v), want 5", n, err)
	}
	left, err := ListAudit(d, "", 10)
	if err != nil || len(left) != 1 || left[0].Action != "audit.purge" || left[0].IP != "10.0.0.1" {
		t.Errorf("after a purge the trail holds %+v (%v), want the purge alone", left, err)
	}
}

func TestPurgeLogs(t *testing.T) {
	d := openTestDB(t)
	if err := AppendLogs(d, "a1", []LogEntry{{Line: "x"}, {Line: "y"}}); err != nil {
		t.Fatal(err)
	}
	if n, err := PurgeLogs(d); err != nil || n != 2 {
		t.Errorf("purged %d (%v), want 2", n, err)
	}
	if n, _ := CountLogs(d, "", ""); n != 0 {
		t.Errorf("%d lines left", n)
	}
}
