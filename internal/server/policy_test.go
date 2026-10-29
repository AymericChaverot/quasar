package server

import (
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// A page goes out with its policy, and every inline script in it carries the
// nonce that policy names — a script without it would not run.
func TestPagesCarryTheirPolicyAndItsNonce(t *testing.T) {
	s, signed, _ := signedIn(t)
	w := httptest.NewRecorder()
	s.render(w, signed, "login", map[string]any{"HideNav": true})

	csp := w.Header().Get("Content-Security-Policy")
	m := regexp.MustCompile(`'nonce-([^']+)'`).FindStringSubmatch(csp)
	if m == nil {
		t.Fatalf("no nonce in the policy %q", csp)
	}
	for _, want := range []string{"default-src 'self'", "object-src 'none'", "frame-ancestors 'self'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("the policy lacks %q", want)
		}
	}
	if strings.Contains(csp, "unsafe-eval") {
		t.Error("the policy allows eval")
	}
	scripts := regexp.MustCompile(`<script(?:\s[^>]*)?>`).FindAllString(w.Body.String(), -1)
	for _, tag := range scripts {
		if strings.Contains(tag, "src=") {
			continue
		}
		if !strings.Contains(tag, `nonce="`+m[1]+`"`) {
			t.Errorf("an inline script without this response's nonce: %s", tag)
		}
	}
	if w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("no nosniff")
	}

	// Each page has a nonce of its own.
	w2 := httptest.NewRecorder()
	s.render(w2, signed, "login", map[string]any{"HideNav": true})
	if w2.Header().Get("Content-Security-Policy") == csp {
		t.Error("two pages were sent the same nonce")
	}
}
