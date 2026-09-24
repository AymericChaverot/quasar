package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"quasar/internal/auth"
	"quasar/internal/db"
)

// Once an address has used up its attempts it is refused — even with the
// right password, which is not checked — while another address signs in.
func TestLoginRefusesAnAddressAfterTooManyFailures(t *testing.T) {
	s := testServer(t)
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	s.db = database
	if err := auth.EnsureAdmin(database, "admin", "correct-horse-battery"); err != nil {
		t.Fatal(err)
	}
	s.loginGuard = newLoginGuard(func() loginPolicy { return loginPolicy{Attempts: 3, Lock: 15 * time.Minute} })

	login := func(ip, password string) *httptest.ResponseRecorder {
		form := url.Values{"username": {"admin"}, "password": {password}}
		r := httptest.NewRequest("POST", "/login", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.RemoteAddr = ip + ":40000"
		w := httptest.NewRecorder()
		s.handleLogin(w, r)
		return w
	}

	for i := 1; i <= 2; i++ {
		if w := login("203.0.113.7", "wrong"); w.Code != http.StatusOK {
			t.Fatalf("failure %d answered %d, want the form again", i, w.Code)
		}
	}
	w := login("203.0.113.7", "wrong")
	if w.Code != http.StatusTooManyRequests || !strings.Contains(w.Body.String(), "Try again in 15 minutes") {
		t.Fatalf("the failure that reached the limit answered %d", w.Code)
	}
	if w := login("203.0.113.7", "correct-horse-battery"); w.Code != http.StatusTooManyRequests {
		t.Fatalf("a refused address signed in with the right password (%d)", w.Code)
	}
	if w.Header().Get("Retry-After") == "" {
		t.Error("no Retry-After on the refusal")
	}
	if w := login("198.51.100.1", "correct-horse-battery"); w.Code != http.StatusSeeOther {
		t.Fatalf("another address was refused too (%d)", w.Code)
	}
}
