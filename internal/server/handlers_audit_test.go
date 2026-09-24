package server

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"quasar/internal/db"
)

// The audit trail is only useful if the recorded origin cannot be chosen by the
// person being recorded. Quasar always sits behind its own Traefik, which
// appends the real peer to X-Forwarded-For, so the last entry is the trusted
// one and anything before it is client-supplied.
func TestClientIP(t *testing.T) {
	tests := []struct {
		name       string
		remoteAddr string
		forwarded  string
		cf         string
		want       string
	}{
		{
			name:       "no proxy header",
			remoteAddr: "203.0.113.9:54321",
			want:       "203.0.113.9",
		},
		{
			name:       "behind traefik",
			remoteAddr: "172.18.0.4:44444",
			forwarded:  "198.51.100.7",
			want:       "198.51.100.7",
		},
		{
			// A client sending its own X-Forwarded-For gets its entry appended
			// to, not replaced — trusting the first would let it claim any
			// address it likes.
			name:       "spoofed prefix is ignored",
			remoteAddr: "172.18.0.4:44444",
			forwarded:  "1.2.3.4, 198.51.100.7",
			want:       "198.51.100.7",
		},
		{
			name:       "spaces around entries",
			remoteAddr: "172.18.0.4:44444",
			forwarded:  "1.2.3.4 ,  198.51.100.7 ",
			want:       "198.51.100.7",
		},
		{
			name:       "ipv6 peer",
			remoteAddr: "[2001:db8::1]:8080",
			want:       "2001:db8::1",
		},
		{
			// Some transports leave RemoteAddr without a port.
			name:       "no port",
			remoteAddr: "203.0.113.9",
			want:       "203.0.113.9",
		},
		{
			name:       "behind cloudflare",
			remoteAddr: "172.18.0.4:44444",
			forwarded:  "162.158.1.20",
			cf:         "198.51.100.7",
			want:       "198.51.100.7",
		},
		{
			name:       "behind cloudflare over ipv6",
			remoteAddr: "172.18.0.4:44444",
			forwarded:  "2606:4700::6810:1",
			cf:         "2001:db8::42",
			want:       "2001:db8::42",
		},
		{
			// From anywhere but Cloudflare the header is only a claim, and
			// believing it would let an attacker choose the address their
			// failed sign-ins are counted against.
			name:       "cloudflare header from elsewhere is ignored",
			remoteAddr: "172.18.0.4:44444",
			forwarded:  "203.0.113.9",
			cf:         "198.51.100.7",
			want:       "203.0.113.9",
		},
		{
			name:       "unreadable cloudflare header",
			remoteAddr: "172.18.0.4:44444",
			forwarded:  "162.158.1.20",
			cf:         "not an address",
			want:       "162.158.1.20",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/apps/x/delete", nil)
			r.RemoteAddr = tc.remoteAddr
			if tc.forwarded != "" {
				r.Header.Set("X-Forwarded-For", tc.forwarded)
			}
			if tc.cf != "" {
				r.Header.Set("CF-Connecting-IP", tc.cf)
			}
			if got := clientIP(r); got != tc.want {
				t.Errorf("clientIP() = %q, want %q", got, tc.want)
			}
		})
	}
}

// The Audit page shows auditPageSize entries, newest first, and the page after
// it the ones before those — with the search carried from one to the next.
func TestAuditPages(t *testing.T) {
	s, database := catalogTestServer(t)
	for i := 1; i <= auditPageSize+3; i++ {
		db.RecordAudit(database, db.AuditEntry{Actor: "admin", Action: "app.deploy", Target: fmt.Sprintf("app-%03d", i)})
	}
	db.RecordAudit(database, db.AuditEntry{Actor: "admin", Action: "login"})

	get := func(target string) string {
		w := httptest.NewRecorder()
		s.handleAuditPage(w, httptest.NewRequest("GET", target, nil))
		return w.Body.String()
	}

	first := get("/audit?q=deploy")
	if !strings.Contains(first, fmt.Sprintf("app-%03d", auditPageSize+3)) || strings.Contains(first, "app-003") {
		t.Error("the first page does not hold the newest entries, and only those")
	}
	if !strings.Contains(first, `href="/audit?page=2&amp;q=deploy"`) {
		t.Error("the first page does not link to the second, search included")
	}

	second := get("/audit?q=deploy&page=2")
	if !strings.Contains(second, "app-003") || strings.Contains(second, "app-004") {
		t.Error("the second page does not hold exactly the three oldest entries")
	}

	// A page past the end, typed into the page field, is the last one.
	if past := get("/audit?q=deploy&page=9"); !strings.Contains(past, "app-003") || !strings.Contains(past, "of 2") {
		t.Error("a page past the end is not the last page")
	}
}
