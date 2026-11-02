package server

import (
	"net/http"
	"slices"
	"strconv"
	"time"

	"quasar/internal/db"
)

// The bell.
//
// The header's bell holds what toasts said and are gone: every alert the
// platform raised, the same ones its channels carried — or would have, had
// one been set up — and how the long jobs this person started ended. Its
// number is what came since they last opened it; opening it reads them all.

// bellPanelSize is how many notifications the bell's panel shows. The
// Notifications page has the rest.
const bellPanelSize = 20

// notificationOrigins are the panel's tabs and the page's first filter.
var notificationOrigins = []struct{ Value, Label string }{
	{"", "All"}, {"alerts", "Alerts"}, {"mine", "Mine"},
}

// notificationKinds are the page's second filter, worst first.
var notificationKinds = []struct{ Value, Label string }{
	{"err", "Errors"}, {"warn", "Warnings"}, {"info", "Info"}, {"ok", "Success"},
}

// NotificationView is one notification, worded.
type NotificationView struct {
	Kind, Title, Text, Detail, Link string
	// Time is the hour, under the day it is filed under.
	Time string
	// Full is the date and time, for the tooltip.
	Full   string
	Unread bool
	// Mine is the end of a job the reader started, rather than an alert.
	Mine bool
}

// NotificationDay is the notifications of one day.
type NotificationDay struct {
	Label string
	Items []NotificationView
}

// NotificationsView is the bell's panel, or the Notifications page.
type NotificationsView struct {
	Days   []NotificationDay
	Counts db.NotificationCounts
	// Newest is the id of the newest notification shown, which is how far
	// showing them marks as read.
	Newest int64

	Origin  string
	Kinds   []string
	Unread  bool
	Origins []NotificationFilterChip
	KindsOf []NotificationFilterChip
}

// NotificationFilterChip is one filter, with the address it leads to.
type NotificationFilterChip struct {
	Label, Href string
	Count       int
	On          bool
}

// groupNotifications files notifications under their day, newest first, in
// the words a person uses for them: today, yesterday, then the date.
func groupNotifications(ns []db.Notification, now time.Time) []NotificationDay {
	var days []NotificationDay
	today := dayStart(now)
	for _, n := range ns {
		at := n.At.In(now.Location())
		var label string
		switch d := today.Sub(dayStart(at)); {
		case d <= 0:
			label = "Today"
		case d <= 24*time.Hour:
			label = "Yesterday"
		case at.Year() == now.Year():
			label = at.Format("Mon 2 Jan")
		default:
			label = at.Format("2 Jan 2006")
		}
		if len(days) == 0 || days[len(days)-1].Label != label {
			days = append(days, NotificationDay{Label: label})
		}
		last := &days[len(days)-1]
		last.Items = append(last.Items, NotificationView{
			Kind: n.Kind, Title: n.Title, Text: n.Text, Detail: n.Detail, Link: n.Link,
			Time: at.Format("15:04"), Full: at.Format("Mon 2 Jan 2006, 15:04"),
			Unread: n.Unread, Mine: n.UserID != 0,
		})
	}
	return days
}

func dayStart(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}

// notificationsFor gathers what user is shown under filter f.
func (s *Server) notificationsFor(user int64, f db.NotificationFilter) (NotificationsView, error) {
	ns, err := db.Notifications(s.db, user, f)
	if err != nil {
		return NotificationsView{}, err
	}
	v := NotificationsView{
		Days:   groupNotifications(ns, time.Now()),
		Counts: db.CountNotifications(s.db, user),
		Origin: f.Origin, Kinds: f.Kinds, Unread: f.Unread,
	}
	if len(ns) > 0 {
		v.Newest = ns[0].ID
	}
	return v, nil
}

// notificationOrigin reads the origin filter, keeping only the words it knows.
func notificationOrigin(r *http.Request) string {
	switch o := r.URL.Query().Get("origin"); o {
	case "alerts", "mine":
		return o
	}
	return ""
}

// handleBellCount is the number on the bell, polled and asked again whenever
// a toast arrives.
func (s *Server) handleBellCount(w http.ResponseWriter, r *http.Request) {
	user, _, _, _ := s.currentUser(r)
	s.renderPartial(w, "bell_count", db.UnreadNotifications(s.db, user))
}

// BellPanel is the bell's panel: its three tabs, each drawn in full, so
// going from one to another is showing it rather than asking for it.
type BellPanel struct {
	Tabs   []BellTab
	Unread int
	// Newest is how far opening the panel marks as read.
	Newest int64
}

// BellTab is one of the panel's tabs and what it lists.
type BellTab struct {
	Value, Label string
	Count        int
	Days         []NotificationDay
}

// handleBellPanel is the bell's panel, drawn as it opens.
func (s *Server) handleBellPanel(w http.ResponseWriter, r *http.Request) {
	user, _, _, _ := s.currentUser(r)
	counts := db.CountNotifications(s.db, user)
	p := BellPanel{Unread: counts.Unread}
	of := map[string]int{"": counts.All, "alerts": counts.Alerts, "mine": counts.Mine}
	for _, o := range notificationOrigins {
		v, err := s.notificationsFor(user, db.NotificationFilter{Origin: o.Value, Limit: bellPanelSize})
		if err != nil {
			http.Error(w, "the notifications could not be read", http.StatusInternalServerError)
			return
		}
		p.Newest = max(p.Newest, v.Newest)
		p.Tabs = append(p.Tabs, BellTab{Value: o.Value, Label: o.Label, Count: of[o.Value], Days: v.Days})
	}
	s.renderPartial(w, "bell_panel", p)
}

// handleNotificationsSeen marks what was shown as read, and has the bell's
// number asked again.
func (s *Server) handleNotificationsSeen(w http.ResponseWriter, r *http.Request) {
	user, _, _, _ := s.currentUser(r)
	upTo, err := strconv.ParseInt(r.FormValue("upto"), 10, 64)
	if err != nil || upTo < 0 {
		http.Error(w, "upto is the id of the newest notification shown", http.StatusBadRequest)
		return
	}
	if err := db.MarkNotificationsSeen(s.db, user, upTo); err != nil {
		http.Error(w, "the notifications could not be marked as read", http.StatusInternalServerError)
		return
	}
	w.Header().Set("HX-Trigger", "quasar:bell")
	w.WriteHeader(http.StatusNoContent)
}

// handleNotifications is the whole history, filtered.
func (s *Server) handleNotifications(w http.ResponseWriter, r *http.Request) {
	user, _, _, _ := s.currentUser(r)
	q := r.URL.Query()
	f := db.NotificationFilter{Origin: notificationOrigin(r), Unread: q.Get("unread") == "1"}
	for _, k := range notificationKinds {
		if slices.Contains(q["kind"], k.Value) {
			f.Kinds = append(f.Kinds, k.Value)
		}
	}
	v, err := s.notificationsFor(user, f)
	if err != nil {
		http.Error(w, "the notifications could not be read", http.StatusInternalServerError)
		return
	}
	v.Origins, v.KindsOf = notificationChips(f, v.Counts)
	s.render(w, r, "notifications", map[string]any{"Title": "Notifications", "N": v})
}

// notificationChips are the page's filters, each leading to the page with it
// turned on or off and the others as they are.
func notificationChips(f db.NotificationFilter, c db.NotificationCounts) (origins, kinds []NotificationFilterChip) {
	href := func(o string, ks []string, unread bool) string {
		q := make([]string, 0, len(ks)+2)
		if o != "" {
			q = append(q, "origin="+o)
		}
		for _, k := range ks {
			q = append(q, "kind="+k)
		}
		if unread {
			q = append(q, "unread=1")
		}
		h := "/notifications"
		for i, p := range q {
			if i == 0 {
				h += "?"
			} else {
				h += "&"
			}
			h += p
		}
		return h
	}
	counts := map[string]int{"": c.All, "alerts": c.Alerts, "mine": c.Mine}
	for _, o := range notificationOrigins {
		origins = append(origins, NotificationFilterChip{
			Label: o.Label, Count: counts[o.Value], On: f.Origin == o.Value,
			Href: href(o.Value, f.Kinds, f.Unread),
		})
	}
	origins = append(origins, NotificationFilterChip{
		Label: "Unread", Count: c.Unread, On: f.Unread, Href: href(f.Origin, f.Kinds, !f.Unread),
	})
	for _, k := range notificationKinds {
		on := slices.Contains(f.Kinds, k.Value)
		var ks []string
		for _, other := range notificationKinds {
			if other.Value == k.Value && !on || other.Value != k.Value && slices.Contains(f.Kinds, other.Value) {
				ks = append(ks, other.Value)
			}
		}
		kinds = append(kinds, NotificationFilterChip{Label: k.Label, On: on, Href: href(f.Origin, ks, f.Unread)})
	}
	return origins, kinds
}
