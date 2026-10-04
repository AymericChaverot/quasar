package server

import (
	"bytes"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Notices.
//
// What an action has to say once it is over — saved, deleted, failed and why —
// is a toast in the corner of whatever page is open, not a line at the top of
// the one it redirected to and not a parameter in its address. An address is
// bookmarked, shared and reloaded, and a "Backup deleted." that comes back with
// every reload is a message that has stopped meaning anything.
//
// There are two ways one gets to a browser. A form that redirects leaves its
// notice for the page it redirects to, which draws it with the rest of the
// page — that is the session's flash, and only the tab that asked sees it. An
// action that goes on after its request has answered says how it ended over the
// stream every page holds open, to every tab of the person who started it; if
// none is open, the notice waits for the next page they load.

// Notice kinds. The words are the template's: each picks a colour, an icon and
// how long the toast stays.
const (
	noticeOK   = "ok"
	noticeInfo = "info"
	noticeWarn = "warn"
	noticeErr  = "err"
)

// Notice is one toast.
type Notice struct {
	Kind  string
	Title string
	// Text is the sentence under the title. Detail, under that, is what a
	// program said — an error as it was returned — and is set in mono so it
	// reads as quoted rather than as the dashboard's own words.
	Text   string
	Detail string
	// ID names a notice a later one may take the place of on screen: "Cleanup
	// started" turning into "Cleanup finished" is one toast that changed, not
	// two stacked on top of each other.
	ID string
}

// heldNotices is how many notices wait for one person at most. A dashboard
// left closed through a night of failing jobs should open on the latest few,
// not on a column of them down the whole screen.
const heldNotices = 5

// noticeBoard holds the notices no page has drawn yet, and the streams that
// can be handed one as soon as it exists.
type noticeBoard struct {
	mu sync.Mutex
	// flash is keyed by session: it is the answer to a request that tab made.
	flash map[string][]Notice
	// held is keyed by user: it is the end of something they started, which
	// no page of theirs was open to hear.
	held map[int64][]Notice
	// subs are the open streams, with when each was opened.
	subs map[int64]map[chan Notice]time.Time
}

func (b *noticeBoard) init() {
	if b.flash == nil {
		b.flash = map[string][]Notice{}
		b.held = map[int64][]Notice{}
		b.subs = map[int64]map[chan Notice]time.Time{}
	}
}

func appendCapped(list []Notice, n Notice) []Notice {
	list = append(list, n)
	if len(list) > heldNotices {
		list = list[len(list)-heldNotices:]
	}
	return list
}

// addFlash leaves a notice for the next page this session draws.
func (b *noticeBoard) addFlash(session string, n Notice) {
	if session == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.init()
	b.flash[session] = appendCapped(b.flash[session], n)
}

// deliver hands a notice to the pages of one user opened since a moment, or
// keeps it for the next one if there is none.
//
// The moment is when the work it reports on started. A page open before then
// is, as often as not, the very page that started it — in the middle of being
// left for the one its form redirects to — and a notice handed to a page that
// is going away is a notice nobody reads.
func (b *noticeBoard) deliver(user int64, n Notice, since time.Time) {
	if user == 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.init()
	sent := false
	for ch, opened := range b.subs[user] {
		if opened.Before(since) {
			continue
		}
		// Buffered and never blocked on: a stream too slow to take one is
		// a tab that is going away.
		select {
		case ch <- n:
			sent = true
		default:
		}
	}
	if !sent {
		b.held[user] = appendCapped(b.held[user], n)
	}
}

// take returns, and forgets, what is waiting for a page about to be drawn.
func (b *noticeBoard) take(session string, user int64) []Notice {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.init()
	out := b.held[user]
	// A task quick enough to have ended before the page saying it started
	// was drawn: the end is what to show, and the start would only take its
	// place on screen.
	for _, n := range b.flash[session] {
		if n.ID == "" || !slices.ContainsFunc(out, func(o Notice) bool { return o.ID == n.ID }) {
			out = append(out, n)
		}
	}
	delete(b.held, user)
	delete(b.flash, session)
	return out
}

// subscribe opens a stream for one of a user's pages. Whatever was being held
// for them comes down it first: a task that ended between the page being drawn
// and its stream opening would otherwise wait for the page after.
func (b *noticeBoard) subscribe(user int64) chan Notice {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.init()
	ch := make(chan Notice, heldNotices)
	for _, n := range b.held[user] {
		ch <- n
	}
	delete(b.held, user)
	if b.subs[user] == nil {
		b.subs[user] = map[chan Notice]time.Time{}
	}
	b.subs[user][ch] = time.Now()
	return ch
}

func (b *noticeBoard) unsubscribe(user int64, ch chan Notice) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.subs[user], ch)
	if len(b.subs[user]) == 0 {
		delete(b.subs, user)
	}
}

func okNotice(title, text string) Notice   { return Notice{Kind: noticeOK, Title: title, Text: text} }
func infoNotice(title, text string) Notice { return Notice{Kind: noticeInfo, Title: title, Text: text} }
func warnNotice(title, text string) Notice { return Notice{Kind: noticeWarn, Title: title, Text: text} }

// errNotice is a failure, with what the program said — when there is one —
// kept apart from the sentence around it.
func errNotice(title, text string, err error) Notice {
	n := Notice{Kind: noticeErr, Title: title, Text: text}
	if err != nil {
		n.Detail = err.Error()
	}
	return n
}

// flash leaves a notice for the page this request is about to redirect to.
func (s *Server) flash(r *http.Request, n Notice) {
	_, _, _, token := s.currentUser(r)
	s.notices.addFlash(token, n)
}

// redirectWith sends the browser on to a page that will draw the notice.
func (s *Server) redirectWith(w http.ResponseWriter, r *http.Request, to string, n Notice) {
	s.flash(r, n)
	http.Redirect(w, r, to, http.StatusSeeOther)
}

// redirectSystem is redirectWith for the System page, where most of the
// platform's own actions are taken.
func (s *Server) redirectSystem(w http.ResponseWriter, r *http.Request, n Notice) {
	s.redirectWith(w, r, "/system", n)
}

// noticeKeepalive is how often an idle notice stream proves it is alive, under
// the minute a proxy is likely to cut a silent connection at.
const noticeKeepalive = 25 * time.Second

// handleNotices is the stream every page holds open, for the ends of the
// things this person started that finish while they are elsewhere.
func (s *Server) handleNotices(w http.ResponseWriter, r *http.Request) {
	user, _, _, _ := s.currentUser(r)
	flusher, ok := w.(http.Flusher)
	if !ok || user == 0 {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ch := s.notices.subscribe(user)
	defer s.notices.unsubscribe(user, ch)
	if !sse(w, ": open\n\n") {
		return
	}
	flusher.Flush()
	for {
		select {
		case n := <-ch:
			if !sse(w, "event: notice\ndata: %s\n\n", s.renderNotice(n)) {
				return
			}
		case <-r.Context().Done():
			return
		case <-time.After(noticeKeepalive):
			if !sse(w, ": still here\n\n") {
				return
			}
		}
		flusher.Flush()
	}
}

// renderNotice draws one toast on a single line, which is what an event's data
// has to be.
func (s *Server) renderNotice(n Notice) string {
	var buf bytes.Buffer
	if err := s.pages["dashboard"].ExecuteTemplate(&buf, "toast", n); err != nil {
		return ""
	}
	return strings.ReplaceAll(buf.String(), "\n", " ")
}

// pendingNotices is what render draws into the page's toast container.
func (s *Server) pendingNotices(r *http.Request) []Notice {
	user, _, _, token := s.currentUser(r)
	if user == 0 && token == "" {
		return nil
	}
	return s.notices.take(token, user)
}

// noticeID names a notice a later one will take the place of.
func noticeID(prefix string) string {
	return prefix + "-" + strconv.FormatInt(time.Now().UnixNano(), 36)
}
