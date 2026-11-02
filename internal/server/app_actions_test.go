package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"quasar/internal/db"
)

// An action sent as a plain form — from the command palette — goes back to
// the page it was sent from with a toast, rather than answering with a panel
// meant for the application's own page.
func TestAnAppActionFromAFormGoesBackWithAToast(t *testing.T) {
	s, r, _ := signedIn(t)
	a := &db.App{ID: "a1", Name: "Blog"}

	r.Header.Set("Referer", "http://"+r.Host+"/logs")
	w := httptest.NewRecorder()
	s.appActionDone(w, r, a, okNotice("Blog restarted", ""))
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/logs" {
		t.Fatalf("got %d to %q", w.Code, w.Header().Get("Location"))
	}
	if got := flashed(s, r); len(got) != 1 || got[0].Title != "Blog restarted" {
		t.Fatalf("flashed %+v", got)
	}

	// From nowhere this dashboard knows, it goes to the application.
	r.Header.Set("Referer", "https://elsewhere.org/")
	if got := actionPage(r, a); got != "/apps/a1" {
		t.Errorf("went to %q", got)
	}
}

// A theme chosen from the palette leaves the reader on the page they were on.
func TestThemeGoesBackWhereItWasChosen(t *testing.T) {
	s, r, _ := signedIn(t)
	for referer, want := range map[string]string{"http://" + r.Host + "/system": "/system", "": "/settings"} {
		req := httptest.NewRequest(http.MethodPost, "/theme", strings.NewReader("theme=paper"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Referer", referer)
		w := httptest.NewRecorder()
		s.handleThemeSet(w, req)
		if w.Header().Get("Location") != want {
			t.Errorf("from %q: went to %q, want %q", referer, w.Header().Get("Location"), want)
		}
	}
}
