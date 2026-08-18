package server

import (
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"quasar/internal/db"
	"quasar/internal/station"
)

// A small station that is accepted as it stands, so the tests can break one
// thing at a time and see only that.
const testStation = `
schema: 1
id: demo
name: Demo
description: A station the tests can lean on
version: "1.0.0"

deploy:
  deploy_type: compose
  compose_service: app
  port: 8080
  compose: |
    services:
      app:
        image: nginx:alpine

permissions:
  exec: {services: [app]}
  net.external: {allow: ["api.example.com"]}

ui:
  tabs:
    - id: main
      name: Main
      panels:
        - {id: hello, type: stat, title: Hello, source: {action: hello}}

script: |
  export function hello() { return { data: { value: 1 } } }
`

// bodyText is the rendered page with its HTML escapes undone, so a test can
// assert on the sentence an operator reads rather than on &#34;.
func bodyText(w *httptest.ResponseRecorder) string {
	return html.UnescapeString(w.Body.String())
}

// install runs the two steps an operator goes through: read the document, then
// accept what it asks for.
func install(t *testing.T, s *Server, doc string) {
	t.Helper()
	st, err := station.Parse(doc)
	if err != nil {
		t.Fatalf("the test document does not parse: %v", err)
	}
	w := post(t, s.handleStationInstall, "/settings/stations", url.Values{
		"yaml": {doc}, "accepted": {st.Permissions.Hash()},
	})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status %d, want a redirect — the document was rejected:\n%s", w.Code, w.Body)
	}
}

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

func TestInstallStoresAnAcceptedStation(t *testing.T) {
	s, database := catalogTestServer(t)
	install(t, s, testStation)

	rows, err := db.ListStations(database)
	if err != nil || len(rows) != 1 {
		t.Fatalf("%d stations stored (%v), want 1", len(rows), err)
	}
	row := rows[0]
	if row.StationID != "demo" || row.Name != "Demo" || !row.Enabled {
		t.Errorf("stored %+v", row)
	}
	if row.YAML != strings.ReplaceAll(testStation, "\r\n", "\n") {
		t.Error("the document was not stored as it was written")
	}
	// What was accepted is what is remembered: a revision asking for anything
	// else has to come back and ask.
	st, _ := station.Parse(testStation)
	if row.PermsHash != st.Permissions.Hash() {
		t.Errorf("perms_hash = %q, want the hash of what was shown", row.PermsHash)
	}
}

// The document is read again when it is installed, so an approval given for
// one set of permissions cannot carry another.
func TestInstallRefusesAnApprovalForSomethingElse(t *testing.T) {
	s, database := catalogTestServer(t)

	swapped := strings.Replace(testStation, `allow: ["api.example.com"]`, `allow: ["evil.example.com"]`, 1)
	st, _ := station.Parse(testStation)
	w := post(t, s.handleStationInstall, "/settings/stations", url.Values{
		"yaml": {swapped}, "accepted": {st.Permissions.Hash()},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status %d, want the page back with the refusal", w.Code)
	}
	if !strings.Contains(w.Body.String(), "not the one whose permissions were shown") {
		t.Errorf("the refusal does not say why:\n%s", w.Body)
	}
	if rows, _ := db.ListStations(database); len(rows) != 0 {
		t.Errorf("%d stations stored despite the mismatch", len(rows))
	}
}

// A station is a program. Two of them under one id, with whichever was
// imported last quietly winning, is how somebody's program gets replaced by
// somebody else's.
func TestASecondStationCannotTakeAnIdInUse(t *testing.T) {
	s, database := catalogTestServer(t)
	install(t, s, testStation)

	other := strings.Replace(testStation, "name: Demo", "name: Something else", 1)
	w := post(t, s.handleStationReview, "/settings/stations/review", url.Values{"yaml": {other}})
	if w.Code != http.StatusOK {
		t.Fatalf("status %d, want the page back with the refusal", w.Code)
	}
	if body := bodyText(w); !strings.Contains(body, `The id "demo" is already held by the station "Demo"`) {
		t.Errorf("the refusal does not name the id or the station already holding it:\n%s", body)
	}
	if rows, _ := db.ListStations(database); len(rows) != 1 {
		t.Errorf("%d stations stored, want the first one only", len(rows))
	}
}

// A document that would not run is not stored, and what is wrong with it comes
// back on the page rather than as one line about the first problem.
func TestAStationThatDoesNotValidateIsNotStored(t *testing.T) {
	s, database := catalogTestServer(t)

	broken := strings.Replace(testStation, "action: hello", "action: helo", 1)
	w := post(t, s.handleStationReview, "/settings/stations/review", url.Values{"yaml": {broken}})
	if w.Code != http.StatusOK {
		t.Fatalf("status %d, want the page back with the problems", w.Code)
	}
	if !strings.Contains(w.Body.String(), "is not exported by the script") {
		t.Errorf("the page does not say what is wrong:\n%s", w.Body)
	}
	if rows, _ := db.ListStations(database); len(rows) != 0 {
		t.Errorf("%d stations stored, want none", len(rows))
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

func TestDeleteRemovesAStation(t *testing.T) {
	s, database := catalogTestServer(t)
	install(t, s, testStation)
	rows, _ := db.ListStations(database)

	req := httptest.NewRequest("POST", "/settings/stations/1/delete", nil)
	req.SetPathValue("id", strconv.FormatInt(rows[0].ID, 10))
	w := httptest.NewRecorder()
	s.handleStationDelete(w, req)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status %d, want a redirect", w.Code)
	}
	if rows, _ := db.ListStations(database); len(rows) != 0 {
		t.Errorf("%d stations left after a delete", len(rows))
	}
}
