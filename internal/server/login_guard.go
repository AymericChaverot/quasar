package server

import (
	"database/sql"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"quasar/internal/db"
	"quasar/internal/event"
)

// Sign-in protection defaults, and the bounds an admin can set them within.
// Fewer than three attempts locks people out over a typo; a lock under a minute
// barely slows a script down.
const (
	defaultLoginAttempts = 5
	defaultLoginLock     = 15 * time.Minute

	minLoginAttempts = 3
	maxLoginAttempts = 100
	minLoginLockMins = 1
	maxLoginLockMins = 24 * 60
)

// loginPolicy is how many failed attempts an address gets, and for how long it
// is then refused.
type loginPolicy struct {
	Attempts int
	Lock     time.Duration
}

// loginPolicyFrom reads the policy from the settings, falling back on the
// default for anything missing or out of bounds.
func loginPolicyFrom(database *sql.DB) loginPolicy {
	p := loginPolicy{Attempts: defaultLoginAttempts, Lock: defaultLoginLock}
	if n, err := strconv.Atoi(db.GetSetting(database, db.SettingLoginAttempts)); err == nil &&
		n >= minLoginAttempts && n <= maxLoginAttempts {
		p.Attempts = n
	}
	if m, err := strconv.Atoi(db.GetSetting(database, db.SettingLoginLockMinutes)); err == nil &&
		m >= minLoginLockMins && m <= maxLoginLockMins {
		p.Lock = time.Duration(m) * time.Minute
	}
	return p
}

// loginGuard refuses sign-ins from an address that has failed too often.
//
// Counted per address rather than per account: counting per account would let
// anybody lock the admin out by guessing at it, while an address that is
// refused only shuts out whoever is behind it. It is kept in memory, so a
// restart forgives everyone — a lock is there to make guessing slow, and
// losing one now and then does not change that.
type loginGuard struct {
	mu     sync.Mutex
	byIP   map[string]*guardEntry
	now    func() time.Time
	policy func() loginPolicy
}

type guardEntry struct {
	failures int
	first    time.Time // the first failure still being counted
	until    time.Time // refused until then; zero when not locked
}

func newLoginGuard(policy func() loginPolicy) *loginGuard {
	return &loginGuard{byIP: map[string]*guardEntry{}, now: time.Now, policy: policy}
}

// blocked reports whether ip is refused right now, and for how much longer.
func (g *loginGuard) blocked(ip string) (time.Duration, bool) {
	if g == nil {
		return 0, false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.prune()
	if e := g.byIP[ip]; e != nil && !e.until.IsZero() {
		return e.until.Sub(g.now()), true
	}
	return 0, false
}

// failed counts a failed attempt from ip, and reports whether it was the one
// that locked it out. Failures are counted over the lock's own length: five
// wrong passwords in a quarter of an hour, by default.
func (g *loginGuard) failed(ip string) (locked bool, p loginPolicy) {
	if g == nil {
		return false, loginPolicy{}
	}
	p = g.policy()
	g.mu.Lock()
	defer g.mu.Unlock()
	g.prune()
	now := g.now()
	e := g.byIP[ip]
	if e != nil && !e.until.IsZero() {
		return false, p // already refused; the lock runs from when it began
	}
	if e == nil || now.Sub(e.first) > p.Lock {
		e = &guardEntry{first: now}
		g.byIP[ip] = e
	}
	e.failures++
	if e.failures >= p.Attempts {
		e.until = now.Add(p.Lock)
		return true, p
	}
	return false, p
}

// succeeded forgets ip's failures: whoever is behind it knows the password.
func (g *loginGuard) succeeded(ip string) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.byIP, ip)
}

// unblock lifts ip's lock, for an admin who knows it is theirs.
func (g *loginGuard) unblock(ip string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	_, ok := g.byIP[ip]
	delete(g.byIP, ip)
	return ok
}

// lockout is one refused address, as the Settings page lists it.
type lockout struct {
	IP        string
	Failures  int
	Remaining time.Duration
}

// lockouts lists the addresses refused right now, longest wait first.
func (g *loginGuard) lockouts() []lockout {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.prune()
	var out []lockout
	for ip, e := range g.byIP {
		if !e.until.IsZero() {
			out = append(out, lockout{IP: ip, Failures: e.failures, Remaining: e.until.Sub(g.now())})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Remaining > out[j].Remaining })
	return out
}

// prune drops locks that have run out and counts too old to matter, on the way
// past, so the map only holds addresses that failed recently. Callers hold the
// lock.
func (g *loginGuard) prune() {
	now := g.now()
	lock := g.policy().Lock
	for ip, e := range g.byIP {
		expired := e.until.IsZero() && now.Sub(e.first) > lock
		if expired || (!e.until.IsZero() && !now.Before(e.until)) {
			delete(g.byIP, ip)
		}
	}
}

// humanWait says how long is left of a lock, in the minutes a person counts in.
func humanWait(d time.Duration) string {
	m := int((d + time.Minute - 1) / time.Minute)
	if m <= 1 {
		return "a minute"
	}
	if m < 120 {
		return strconv.Itoa(m) + " minutes"
	}
	return strconv.Itoa((m+59)/60) + " hours"
}

// signInData is what the Settings page shows of the sign-in protection.
func (s *Server) signInData() map[string]any {
	p := loginPolicyFrom(s.db)
	return map[string]any{
		"Attempts":    p.Attempts,
		"LockMinutes": int(p.Lock / time.Minute),
		"MinAttempts": minLoginAttempts,
		"MaxAttempts": maxLoginAttempts,
		"MinLock":     minLoginLockMins,
		"MaxLock":     maxLoginLockMins,
		"Lockouts":    s.loginGuard.lockouts(),
	}
}

// handleSignInProtection stores how many attempts an address gets and how
// long it is then refused.
func (s *Server) handleSignInProtection(w http.ResponseWriter, r *http.Request) {
	attempts, err1 := strconv.Atoi(strings.TrimSpace(r.FormValue("attempts")))
	minutes, err2 := strconv.Atoi(strings.TrimSpace(r.FormValue("lock_minutes")))
	if err1 != nil || err2 != nil ||
		attempts < minLoginAttempts || attempts > maxLoginAttempts ||
		minutes < minLoginLockMins || minutes > maxLoginLockMins {
		s.settingsError(w, r, fmt.Sprintf("Sign-in protection: between %d and %d attempts, locked for %d to %d minutes.",
			minLoginAttempts, maxLoginAttempts, minLoginLockMins, maxLoginLockMins))
		return
	}
	for key, v := range map[string]int{db.SettingLoginAttempts: attempts, db.SettingLoginLockMinutes: minutes} {
		if err := db.SetSetting(s.db, key, strconv.Itoa(v)); err != nil {
			s.settingsError(w, r, "Saving the sign-in protection: "+err.Error())
			return
		}
	}
	detail := fmt.Sprintf("%d attempts, then %d minutes", attempts, minutes)
	s.audit(r, "settings.sign-in", "", detail)
	event.Info("security", "sign-in protection set to "+detail, "by "+s.actor(r))
	http.Redirect(w, r, "/settings?saved=1#sign-in", http.StatusSeeOther)
}

// handleSignInUnblock lifts one address's lock, for an admin who knows it is
// theirs or someone they trust.
func (s *Server) handleSignInUnblock(w http.ResponseWriter, r *http.Request) {
	ip := strings.TrimSpace(r.FormValue("ip"))
	if s.loginGuard.unblock(ip) {
		s.audit(r, "login.unblock", ip, "")
		event.Info("security", ip+" unblocked", "by "+s.actor(r))
	}
	http.Redirect(w, r, "/settings#sign-in", http.StatusSeeOther)
}

// Wait is how long is left of the lock, as the Settings page says it.
func (l lockout) Wait() string { return humanWait(l.Remaining) }
