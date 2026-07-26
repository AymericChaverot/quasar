package monitor

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProbe(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		healthy bool
	}{
		{"ok", http.StatusOK, true},
		{"no content", http.StatusNoContent, true},
		{"redirect", http.StatusFound, true},
		// A mistyped health path 404s. Counting that as healthy meant the app
		// was never checked again in any meaningful way, and auto-restart
		// could never fire.
		{"wrong health path", http.StatusNotFound, false},
		{"unauthorized", http.StatusUnauthorized, false},
		{"crashed", http.StatusInternalServerError, false},
		{"gateway down", http.StatusBadGateway, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()
			if got := probe(srv.URL); got != tc.healthy {
				t.Errorf("probe on %d = %v, want %v", tc.status, got, tc.healthy)
			}
		})
	}
}

func TestProbeUnreachable(t *testing.T) {
	if probe("http://127.0.0.1:1/health") {
		t.Error("a refused connection must not count as healthy")
	}
}
