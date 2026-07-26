package server

import (
	"net/http/httptest"
	"testing"
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
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/apps/x/delete", nil)
			r.RemoteAddr = tc.remoteAddr
			if tc.forwarded != "" {
				r.Header.Set("X-Forwarded-For", tc.forwarded)
			}
			if got := clientIP(r); got != tc.want {
				t.Errorf("clientIP() = %q, want %q", got, tc.want)
			}
		})
	}
}
