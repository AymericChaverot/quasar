package server

import (
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLoginFailuresLogFirstThenSummary(t *testing.T) {
	var mu sync.Mutex
	var lines []string
	f := newLoginFailures()
	f.window = 50 * time.Millisecond
	f.log = func(area string, details ...string) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, area+" "+strings.Join(details, " · "))
	}

	f.record("203.0.113.7", "sign-in", "admin")
	f.record("203.0.113.7", "sign-in", "admin")
	f.record("203.0.113.7", "sign-in", "admin")
	f.record("198.51.100.1", "sign-in", "bob")

	mu.Lock()
	if len(lines) != 2 {
		t.Fatalf("before the window closes want 2 lines, got %q", lines)
	}
	mu.Unlock()

	time.Sleep(150 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	want := []string{
		`login failed sign-in for "admin" · from 203.0.113.7`,
		`login failed sign-in for "bob" · from 198.51.100.1`,
		`login 2 more failed attempts in 50ms · for "admin" · from 203.0.113.7`,
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
}
