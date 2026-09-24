package server

import (
	"fmt"
	"sync"
	"time"

	"quasar/internal/event"
)

// failureWindow is how long the failed sign-ins from one address are counted
// together before they are summed up in the log.
const failureWindow = 10 * time.Minute

// loginFailures keeps a password attack from filling the log a line per guess.
// The first failure from an address is logged at once — it may be someone who
// mistyped, and it may be the start of something — and the ones after it within
// the window are counted and written as a single line when the window closes.
type loginFailures struct {
	mu     sync.Mutex
	window time.Duration
	byIP   map[string]*failureRun
	// log writes a line; event.Warning outside tests.
	log func(area string, details ...string)
}

type failureRun struct {
	more  int             // failures after the first
	users map[string]bool // names tried, for the summary
}

func newLoginFailures() *loginFailures {
	return &loginFailures{window: failureWindow, byIP: map[string]*failureRun{}, log: event.Warning}
}

// record notes one failed attempt at what ("sign-in", "2FA code") for user
// from ip. A server built without a tracker logs nothing.
func (f *loginFailures) record(ip, what, user string) {
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if run, ok := f.byIP[ip]; ok {
		run.more++
		run.users[user] = true
		return
	}
	f.byIP[ip] = &failureRun{users: map[string]bool{}}
	f.log("login", fmt.Sprintf("failed %s for %q", what, user), "from "+ip)
	time.AfterFunc(f.window, func() { f.close(ip) })
}

func (f *loginFailures) close(ip string) {
	f.mu.Lock()
	run := f.byIP[ip]
	delete(f.byIP, ip)
	f.mu.Unlock()
	if run == nil || run.more == 0 {
		return
	}
	users := ""
	if len(run.users) > 1 {
		users = fmt.Sprintf("%d different usernames", len(run.users))
	} else {
		for u := range run.users {
			users = fmt.Sprintf("for %q", u)
		}
	}
	f.log("login", fmt.Sprintf("%d more failed attempts in %s", run.more, f.window), users, "from "+ip)
}
