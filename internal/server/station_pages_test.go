package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"quasar/internal/db"
)

// Reading a document and showing what it asks for — the half of the import
// that stores nothing. See station_install_test.go for the half that does.

// Reading a document shows what it would be allowed to do, in words, and
// stores nothing. Consent to something unreadable is not consent, and an
// approval screen that has already installed the thing is decoration.
func TestReviewShowsThePermissionsAndStoresNothing(t *testing.T) {
	s, database := catalogTestServer(t)

	w := post(t, s.handleStationReview, "/settings/stations/review", url.Values{"yaml": {testStation}})
	if w.Code != http.StatusOK {
		t.Fatalf("status %d, want the review page:\n%s", w.Code, w.Body)
	}
	// In words, and naming what each one narrows to: "net.external" is a YAML
	// key, and "may reach api.example.com" is a decision somebody can make.
	body := bodyText(w)
	for _, want := range []string{"Run any command inside the container", "api.example.com", "Accept and install"} {
		if !strings.Contains(body, want) {
			t.Errorf("the review does not say %q", want)
		}
	}

	rows, _ := db.ListStations(database)
	if len(rows) != 0 {
		t.Errorf("%d stations stored by a screen that only reads", len(rows))
	}
}

// What the operator typed comes back with the errors. A document somebody
// spent a while on is not something to throw away over a typo in it.
func TestARejectedDocumentComesBack(t *testing.T) {
	s, _ := catalogTestServer(t)

	broken := strings.Replace(testStation, "schema: 1", "schema: 9", 1)
	w := post(t, s.handleStationReview, "/settings/stations/review", url.Values{"yaml": {broken}})
	if !strings.Contains(w.Body.String(), "id: demo") {
		t.Errorf("the document was not handed back:\n%s", w.Body)
	}
}

// The settings page is reached to add a station far more often than to go back
// to one already installed, and it used to open with the list. Adding comes
// first now, with the screenful of a pasted document folded away behind the
// one-line way in.
func TestTheSettingsPageLeadsWithAddingAStation(t *testing.T) {
	s, _ := catalogTestServer(t)
	install(t, s, testStationWithParams, "")

	r := httptest.NewRequest("GET", "/settings/stations", nil)
	w := httptest.NewRecorder()
	s.handleStations(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d, want the settings page:\n%s", w.Code, w.Body)
	}

	body := bodyText(w)
	add, installed := strings.Index(body, "Add a station"), strings.Index(body, "Installed (")
	if add < 0 || installed < 0 {
		t.Fatalf("the page has no add section or no count:\n%s", body)
	}
	if add > installed {
		t.Error("the page still opens with the stations already installed")
	}
	if !strings.Contains(body, "Installed (1)") {
		t.Error("the heading does not say how many are installed")
	}
	// The textarea is a screenful, and it is the rarer of the two ways in.
	if i := strings.Index(body, "Write or paste one instead"); i < 0 || i > installed {
		t.Error("pasting a document is not the secondary way in")
	}
}
