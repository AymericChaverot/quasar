package server

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"quasar/internal/auth"
	"quasar/internal/db"
)

// signedIn is a server with one admin, and a request carrying their session.
func signedIn(t *testing.T) (*Server, *http.Request, int64) {
	t.Helper()
	s := testServer(t)
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	s.db = database
	if err := auth.CreateUser(database, "ada", "correct horse battery", auth.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	token, _, err := auth.Login(database, "ada", "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/system/cleanup", nil)
	r.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: token})
	user, _, _, _ := s.currentUser(r)
	if user == 0 {
		t.Fatal("the session does not resolve to a user")
	}
	return s, r, user
}

// A task says it has started on the page the request lands on, and says how it
// went to the person who started it once it is done — as the same notice, so
// the one can take the other's place on screen.
func TestATaskReportsToWhoeverStartedIt(t *testing.T) {
	s, r, user := signedIn(t)
	ch := s.notices.subscribe(user)
	defer s.notices.unsubscribe(user, ch)

	release := make(chan struct{})
	if !s.startTask(r, "sweep", infoNotice("Sweep started", ""), func() Notice {
		<-release
		return okNotice("Sweep finished", "1.2 GB freed")
	}) {
		t.Fatal("the task did not start")
	}

	flash := flashed(s, r)
	if len(flash) != 1 || flash[0].Kind != noticeInfo || flash[0].ID == "" {
		t.Fatalf("no started notice: %v", flash)
	}

	// The same task is not started on top of itself.
	if s.startTask(r, "sweep", infoNotice("Sweep started", ""), func() Notice { return Notice{} }) {
		t.Error("a second sweep started while the first was running")
	}

	close(release)
	select {
	case n := <-ch:
		if n.Title != "Sweep finished" || n.ID != flash[0].ID {
			t.Errorf("got %+v, want the finished notice under %q", n, flash[0].ID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the end of the task never arrived")
	}

	// Once it is over, it can be run again.
	deadline := time.Now().Add(2 * time.Second)
	for s.tasks.busy("sweep") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if s.tasks.busy("sweep") {
		t.Error("the task is still marked as running")
	}
}

// A page opened while the task was ending — drawn before the end, its stream
// opened after — still hears of it, rather than on the page after.
func TestAStreamOpensOnWhatWasHeld(t *testing.T) {
	var b noticeBoard
	b.deliver(3, Notice{Title: "Backup created"}, time.Time{})
	ch := b.subscribe(3)
	select {
	case n := <-ch:
		if n.Title != "Backup created" {
			t.Errorf("got %v", n)
		}
	default:
		t.Fatal("the held notice was not sent down the new stream")
	}
	if got := b.take("", 3); len(got) != 0 {
		t.Errorf("it was sent and kept as well: %v", got)
	}
}

// A task that ended before the page saying it had started was drawn shows its
// end, not its start: the start names the same notice and would take its place.
func TestAQuickTaskShowsItsEndNotItsStart(t *testing.T) {
	var b noticeBoard
	b.addFlash("tab", Notice{Kind: noticeInfo, Title: "Cleanup started", ID: "cleanup-1"})
	b.addFlash("tab", Notice{Kind: noticeOK, Title: "Saved"})
	b.deliver(1, Notice{Kind: noticeErr, Title: "Cleanup failed", ID: "cleanup-1"}, time.Time{})
	got := b.take("tab", 1)
	if len(got) != 2 || got[0].Title != "Cleanup failed" || got[1].Title != "Saved" {
		t.Errorf("got %v", got)
	}
}

// The page that started a task is usually on its way out when a quick task
// ends; its stream is not told, and the end waits for the page it is going to.
func TestTheEndOfATaskSkipsThePageThatStartedIt(t *testing.T) {
	var b noticeBoard
	leaving := b.subscribe(1)
	since := time.Now().Add(time.Millisecond)
	b.deliver(1, Notice{Title: "Cleanup failed"}, since)
	select {
	case n := <-leaving:
		t.Errorf("the page being left was told: %v", n)
	default:
	}
	b.unsubscribe(1, leaving)
	arriving := b.subscribe(1)
	select {
	case n := <-arriving:
		if n.Title != "Cleanup failed" {
			t.Errorf("got %v", n)
		}
	default:
		t.Error("the page arrived at was not told")
	}
}

// The stream a page holds open carries the end of a task as a drawn toast,
// as soon as the task is over.
func TestTheStreamCarriesAFinishedTask(t *testing.T) {
	s, r, user := signedIn(t)
	srv := httptest.NewServer(http.HandlerFunc(s.handleNotices))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	for _, c := range r.Cookies() {
		req.AddCookie(c)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}

	// Wait for the stream to be open before the task ends, as a page would be.
	buf := make([]byte, 4096)
	if _, err := resp.Body.Read(buf); err != nil {
		t.Fatal(err)
	}
	s.notices.deliver(user, okNotice("Cleanup finished", "Removed 3 images, freeing about 1.2 GB."), time.Time{})

	got := ""
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(got, "1.2 GB") && time.Now().Before(deadline) {
		n, err := resp.Body.Read(buf)
		got += string(buf[:n])
		if err != nil {
			break
		}
	}
	if !strings.Contains(got, "event: notice") || !strings.Contains(got, "toast-ok") || !strings.Contains(got, "1.2 GB") {
		t.Errorf("the stream said:\n%s", got)
	}
}
