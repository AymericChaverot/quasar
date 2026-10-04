package server

import (
	"net/http"
	"strconv"
	"sync"
	"time"
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
	since := time.Now()
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
