package server

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"quasar/internal/db"
)

// Notifications are filed under today, yesterday, then their date.
func TestGroupNotifications(t *testing.T) {
	now := time.Date(2026, 11, 2, 15, 0, 0, 0, time.UTC)
	ns := []db.Notification{
		{At: now.Add(-time.Hour), Title: "a"},
		{At: now.Add(-14 * time.Hour), Title: "b"},
		{At: now.Add(-16 * time.Hour), Title: "c"},
		{At: now.Add(-60 * time.Hour), Title: "d"},
		{At: time.Date(2025, 12, 31, 9, 0, 0, 0, time.UTC), Title: "e", UserID: 3},
	}
	days := groupNotifications(ns, now)
	var got []string
	for _, d := range days {
		got = append(got, d.Label+":"+itemsOf(d))
	}
	want := "Today:ab Yesterday:c Sat 31 Oct:d 31 Dec 2025:e"
	if strings.Join(got, " ") != want {
		t.Fatalf("got %q, want %q", strings.Join(got, " "), want)
	}
	if !days[3].Items[0].Mine || days[0].Items[0].Mine || days[0].Items[0].Time != "14:00" {
		t.Fatalf("items %+v / %+v", days[3].Items[0], days[0].Items[0])
	}
}

func itemsOf(d NotificationDay) string {
	s := ""
	for _, i := range d.Items {
		s += i.Title
	}
	return s
}

// The bell counts what is new, its panel shows it marked as new and asks for
// it to be read, and once read the number is gone.
func TestTheBellReadsWhatItShows(t *testing.T) {
	s, r, user := signedIn(t)
	cookie := r.Cookies()[0]
	get := func(method, target string, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, target, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		req.AddCookie(cookie)
		w := httptest.NewRecorder()
		s.mux.ServeHTTP(w, req)
		return w
	}
	s.mux = http.NewServeMux()
	s.guards = map[string]string{}
	s.viewer("GET /partials/bell-count", s.handleBellCount)
	s.viewer("GET /partials/bell", s.handleBellPanel)
	s.viewer("POST /notifications/seen", s.handleNotificationsSeen)
	s.viewer("GET /notifications", s.handleNotifications)

	_ = db.AddNotification(s.db, db.Notification{Kind: "err", Title: "photos failed to deploy", Detail: "exit status 1", Link: "/apps/photos"})
	_ = db.AddNotification(s.db, db.Notification{UserID: user, Kind: "ok", Title: "Backup created"})
	_ = db.AddNotification(s.db, db.Notification{UserID: user + 1, Kind: "ok", Title: "Somebody else's cleanup"})

	if body := get("GET", "/partials/bell-count", "").Body.String(); !strings.Contains(body, ">2</span>") {
		t.Fatalf("count: %s", body)
	}
	panel := get("GET", "/partials/bell", "").Body.String()
	for _, want := range []string{"photos failed to deploy", "Backup created", "is-unread", `href="/apps/photos"`, "/notifications/seen", "You started it"} {
		if !strings.Contains(panel, want) {
			t.Errorf("the panel does not show %q", want)
		}
	}
	if strings.Contains(panel, "Somebody else") {
		t.Error("the panel shows somebody else's job")
	}
	mine := get("GET", "/partials/bell?origin=mine", "").Body.String()
	if strings.Contains(mine, "photos failed") || !strings.Contains(mine, "Backup created") {
		t.Errorf("the Mine tab: %s", mine)
	}

	newest, _ := db.Notifications(s.db, user, db.NotificationFilter{Limit: 1})
	w := get("POST", "/notifications/seen", "upto="+strconv.FormatInt(newest[0].ID, 10))
	if w.Code != http.StatusNoContent || w.Header().Get("HX-Trigger") != "quasar:bell" {
		t.Fatalf("seen: %d %v", w.Code, w.Header())
	}
	if body := get("GET", "/partials/bell-count", "").Body.String(); strings.Contains(body, ">2<") || !strings.Contains(body, "></span>") {
		t.Fatalf("count after reading: %s", body)
	}
	if strings.Contains(get("GET", "/partials/bell", "").Body.String(), "/notifications/seen") {
		t.Error("a panel with nothing new still asks to be read")
	}
	if w := get("POST", "/notifications/seen", "upto=x"); w.Code != http.StatusBadRequest {
		t.Errorf("a bad upto: %d", w.Code)
	}

	page := get("GET", "/notifications?origin=alerts&kind=err", "")
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "photos failed to deploy") || strings.Contains(page.Body.String(), "Backup created") {
		t.Fatalf("page: %d", page.Code)
	}
}
