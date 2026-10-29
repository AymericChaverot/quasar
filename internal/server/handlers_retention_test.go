package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"quasar/internal/db"
)

// retentionRequest is a POST from the signed-in admin of signedIn.
func retentionRequest(signed *http.Request, path string, form url.Values) *http.Request {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, c := range signed.Cookies() {
		r.AddCookie(c)
	}
	return r
}

// Saving the windows stores them and applies them straight away; a window
// out of bounds is refused and changes nothing.
func TestRetentionIsSavedAndApplied(t *testing.T) {
	s, signed, _ := signedIn(t)
	old := time.Now().AddDate(0, 0, -3)
	if err := db.AppendLogs(s.db, "a1", []db.LogEntry{{TS: old, Line: "three days old"}, {Line: "fresh"}}); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	s.handleRetention(w, retentionRequest(signed, "/settings/retention", url.Values{"log_days": {"0"}, "audit_days": {"0"}}))
	if db.GetSetting(s.db, db.SettingLogRetentionDays) != "" {
		t.Error("a log window of 0 days was stored")
	}

	w = httptest.NewRecorder()
	s.handleRetention(w, retentionRequest(signed, "/settings/retention", url.Values{"log_days": {"2"}, "audit_days": {"90"}}))
	if w.Code != http.StatusSeeOther || !strings.HasSuffix(w.Header().Get("Location"), "#retention") {
		t.Fatalf("answered %d, to %q", w.Code, w.Header().Get("Location"))
	}
	if db.LogRetentionDays(s.db) != 2 || db.AuditRetentionDays(s.db) != 90 {
		t.Errorf("stored %d and %d days", db.LogRetentionDays(s.db), db.AuditRetentionDays(s.db))
	}
	if n, _ := db.CountLogs(s.db, "", ""); n != 1 {
		t.Errorf("%d lines left after shortening the window to two days, want the fresh one", n)
	}
	if entries, _ := db.ListAudit(s.db, "settings.retention", 5); len(entries) != 1 {
		t.Error("the change is not in the audit trail")
	}
}

func TestPurgesEmptyLogsAndAudit(t *testing.T) {
	s, signed, _ := signedIn(t)
	if err := db.AppendLogs(s.db, "a1", []db.LogEntry{{Line: "x"}, {Line: "y"}}); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := db.RecordAudit(s.db, db.AuditEntry{Actor: "ada", Action: "app.deploy"}); err != nil {
			t.Fatal(err)
		}
	}

	s.handlePurgeLogs(httptest.NewRecorder(), retentionRequest(signed, "/settings/logs/purge", nil))
	if n, _ := db.CountLogs(s.db, "", ""); n != 0 {
		t.Errorf("%d lines left after deleting all logs", n)
	}

	s.handlePurgeAudit(httptest.NewRecorder(), retentionRequest(signed, "/settings/audit/purge", nil))
	left, _ := db.ListAudit(s.db, "", 10)
	if len(left) != 1 || left[0].Action != "audit.purge" || left[0].Actor != "ada" {
		t.Errorf("after emptying the trail it holds %+v, want the purge by ada alone", left)
	}
	if !strings.Contains(left[0].Detail, "4 earlier entries") {
		t.Errorf("the purge says %q, want the four entries it deleted (three deploys and the log purge)", left[0].Detail)
	}
}

func TestRetentionRoutesAreAdminOnly(t *testing.T) {
	s := newTestServer(t)
	for _, route := range []string{"POST /settings/retention", "POST /settings/logs/purge", "POST /settings/audit/purge"} {
		if got := s.guards[route]; got != accessAdmin {
			t.Errorf("%s is %q, want admin", route, got)
		}
	}
}
