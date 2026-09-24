package server

import (
	"testing"
	"time"
)

func testGuard(p loginPolicy) (*loginGuard, *time.Time) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	g := newLoginGuard(func() loginPolicy { return p })
	g.now = func() time.Time { return now }
	return g, &now
}

// The attempt that reaches the limit is the one that locks, for as long as the
// policy says, and only that address.
func TestLoginGuardLocksAfterTheLimit(t *testing.T) {
	g, now := testGuard(loginPolicy{Attempts: 3, Lock: 15 * time.Minute})
	for i := 1; i <= 2; i++ {
		if locked, _ := g.failed("203.0.113.7"); locked {
			t.Fatalf("locked after %d failures", i)
		}
	}
	if _, blocked := g.blocked("203.0.113.7"); blocked {
		t.Fatal("blocked before the limit")
	}
	if locked, _ := g.failed("203.0.113.7"); !locked {
		t.Fatal("the third failure did not lock")
	}
	if left, blocked := g.blocked("203.0.113.7"); !blocked || left != 15*time.Minute {
		t.Fatalf("blocked = %v, %v; want true, 15m", blocked, left)
	}
	if _, blocked := g.blocked("198.51.100.1"); blocked {
		t.Fatal("another address was blocked")
	}

	*now = now.Add(15 * time.Minute)
	if _, blocked := g.blocked("203.0.113.7"); blocked {
		t.Fatal("still blocked once the lock ran out")
	}
	if locked, _ := g.failed("203.0.113.7"); locked {
		t.Fatal("a lock that ran out counted towards the next one")
	}
}

// Failures spread out over longer than the lock are not an attack, and a
// correct password clears the count.
func TestLoginGuardForgets(t *testing.T) {
	g, now := testGuard(loginPolicy{Attempts: 3, Lock: 10 * time.Minute})
	g.failed("203.0.113.7")
	g.failed("203.0.113.7")
	*now = now.Add(11 * time.Minute)
	if locked, _ := g.failed("203.0.113.7"); locked {
		t.Fatal("old failures still counted")
	}

	g.failed("203.0.113.7")
	g.succeeded("203.0.113.7")
	g.failed("203.0.113.7")
	if locked, _ := g.failed("203.0.113.7"); locked {
		t.Fatal("a successful sign-in did not clear the count")
	}
}

func TestLoginGuardLockoutsAndUnblock(t *testing.T) {
	g, now := testGuard(loginPolicy{Attempts: 3, Lock: 15 * time.Minute})
	for range 3 {
		g.failed("203.0.113.7")
	}
	*now = now.Add(5 * time.Minute)
	for range 3 {
		g.failed("198.51.100.1")
	}
	g.failed("192.0.2.9") // counted, not locked

	got := g.lockouts()
	if len(got) != 2 || got[0].IP != "198.51.100.1" || got[1].IP != "203.0.113.7" || got[1].Remaining != 10*time.Minute {
		t.Fatalf("lockouts() = %+v", got)
	}
	if !g.unblock("203.0.113.7") {
		t.Fatal("unblock reported nothing to lift")
	}
	if _, blocked := g.blocked("203.0.113.7"); blocked {
		t.Fatal("still blocked after unblock")
	}
}

func TestHumanWait(t *testing.T) {
	for d, want := range map[time.Duration]string{
		10 * time.Second:                "a minute",
		time.Minute:                     "a minute",
		14*time.Minute + 10*time.Second: "15 minutes",
		3*time.Hour + 20*time.Minute:    "4 hours",
	} {
		if got := humanWait(d); got != want {
			t.Errorf("humanWait(%v) = %q, want %q", d, got, want)
		}
	}
}
