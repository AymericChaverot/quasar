package server

import (
	"net/http"
	"strconv"
	"sync"
	"time"

	"quasar/internal/db"
)

// Background tasks.
//
// A cleanup that deletes gigabytes of layers, a backup, a restore: work that
// takes longer than anybody should sit and watch a button for. The request
// that asks for one starts it and answers straight away with a toast saying it
// has started; the person who asked is free to go anywhere in the dashboard,
// and the toast saying how it went finds them there — the same toast, if the
// first is still on screen, since it names the same notice.

// taskRuns knows which tasks are running, so the same one is not started twice
// on top of itself.
type taskRuns struct {
	mu      sync.Mutex
	running map[string]bool
	seq     int
}

// claim marks a task as running, or reports that it already is.
func (t *taskRuns) claim(key string) (id string, ok bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.running == nil {
		t.running = map[string]bool{}
	}
	if t.running[key] {
		return "", false
	}
	t.running[key] = true
	t.seq++
	return key + "-" + strconv.Itoa(t.seq), true
}

func (t *taskRuns) release(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.running, key)
}

// busy reports whether a task is running, for a card that wants to say so.
func (t *taskRuns) busy(key string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.running[key]
}

// startTask runs work in the background on behalf of whoever made the request.
// started is flashed for the page the request redirects to; whatever work
// returns is delivered to that person when it is done, in place of it. If the
// task is already running, nothing is started and false is returned.
//
// work must not use the request: it is answered before work is half done.
func (s *Server) startTask(r *http.Request, key string, started Notice, work func() Notice) bool {
	id, ok := s.tasks.claim(key)
	if !ok {
		return false
	}
	user, _, _, _ := s.currentUser(r)
	since := noticeSince(r)
	started.ID = id
	s.flash(r, started)
	go func() {
		defer s.tasks.release(key)
		n := work()
		n.ID = id
		s.notices.deliver(user, n, since)
	}()
	return true
}

// noticeSince is the moment the end of what this request starts should be
// told to pages opened after. A form that redirects is leaving the page it
// was sent from, so only the pages after it count. A button that htmx sends
// leaves the page where it is, and that page is the first that should hear.
func noticeSince(r *http.Request) time.Time {
	if r.Header.Get("HX-Request") == "true" {
		return time.Time{}
	}
	return time.Now()
}

// followDeploy tells whoever set off a deploy how it ended, once it has. The
// progress panel follows it on the application's own page; this is for
// wherever else they have gone by then.
func (s *Server) followDeploy(r *http.Request, a *db.App) {
	user, _, _, _ := s.currentUser(r)
	since := noticeSince(r)
	first, _ := s.dock.WatchDeploy(a.ID, -1, -1)
	gen, name, id := first.Gen, a.Name, a.ID
	go func() {
		snap, changed := s.dock.WatchDeploy(id, gen, -1)
		deadline := time.After(deployFollowLimit)
		for snap.Gen == gen && snap.Running {
			select {
			case <-changed:
			case <-deadline:
				return
			}
			snap, changed = s.dock.WatchDeploy(id, gen, snap.Seq)
		}
		// Another deploy of the same application took over: that one has a
		// toast of its own coming.
		if snap.Gen != gen {
			return
		}
		n := okNotice(name+" deployed", "")
		if snap.Err != "" {
			n = Notice{Kind: noticeErr, Title: name + " failed to deploy", Text: "The deploy log is on the application's page.", Detail: snap.Err}
		}
		s.notices.deliver(user, n, since)
	}()
}

// deployFollowLimit is how long followDeploy waits before giving up on a
// deploy that never reports its end, which is longer than any deploy is
// allowed to run.
const deployFollowLimit = 2 * time.Hour
