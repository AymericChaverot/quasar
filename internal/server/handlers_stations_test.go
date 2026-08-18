package server

import (
	"database/sql"
	"html"
	"io"
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
func install(t *testing.T, s *Server, doc, sourceURL string) {
	t.Helper()
	st, err := station.Parse(doc)
	if err != nil {
		t.Fatalf("the test document does not parse: %v", err)
	}
	w := post(t, s.handleStationInstall, "/settings/stations", url.Values{
		"yaml": {doc}, "source_url": {sourceURL}, "accepted": {st.Permissions.Hash()},
	})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status %d, want a redirect — the document was rejected:\n%s", w.Code, w.Body)
	}
}

// postStation calls one of the handlers that act on an installed station,
// which take their subject from the path.
func postStation(t *testing.T, h http.HandlerFunc, id int64) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("POST", "/settings/stations/x", nil)
	r.SetPathValue("id", strconv.FormatInt(id, 10))
	w := httptest.NewRecorder()
	h(w, r)
	return w
}

// origin is an address serving whatever the test currently wants a re-fetch to
// find there, which is the situation the holding rule exists for: the document
// at a URL is not the document that was approved.
func origin(t *testing.T, doc *string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, *doc)
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/station.yaml"
}

// only returns the single installed station, and fails if there is not exactly
// one.
func only(t *testing.T, database *sql.DB) *db.Station {
	t.Helper()
	rows, err := db.ListStations(database)
	if err != nil || len(rows) != 1 {
		t.Fatalf("%d stations stored (%v), want 1", len(rows), err)
	}
	return rows[0]
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
	install(t, s, testStation, "")

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
	install(t, s, testStation, "")

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
	install(t, s, testStation, "")

	if w := postStation(t, s.handleStationDelete, only(t, database).ID); w.Code != http.StatusSeeOther {
		t.Fatalf("status %d, want a redirect", w.Code)
	}
	if rows, _ := db.ListStations(database); len(rows) != 0 {
		t.Errorf("%d stations left after a delete", len(rows))
	}
}

// A station deploys down the catalogue's own path: its deploy block is a
// catalogue entry, so the prefilled form is the one every other application
// arrives at, and what makes it a station is the one hidden field saying so.
func TestDeployingAStationPrefillsItsDeployBlock(t *testing.T) {
	s, _ := catalogTestServer(t)
	install(t, s, testStation, "")

	r := httptest.NewRequest("GET", "/apps/new?station=demo", nil)
	w := httptest.NewRecorder()
	s.handleAppNew(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d, want the prefilled form", w.Code)
	}

	body := bodyText(w)
	for _, want := range []string{
		`name="station_id" value="demo"`, // where the tabs will come from
		"nginx:alpine",                   // the compose file, in the textarea
		`value="app"`,                    // the service the domain routes to
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the form was not prefilled from the station: no %q", want)
		}
	}
}

// A station nobody installed prefills nothing rather than half a form. The id
// arrives in a query string, and a query string is typed by anybody.
func TestAnUnknownStationPrefillsNothing(t *testing.T) {
	s, _ := catalogTestServer(t)

	r := httptest.NewRequest("GET", "/apps/new?station=nothing", nil)
	w := httptest.NewRecorder()
	s.handleAppNew(w, r)
	if strings.Contains(bodyText(w), `name="station_id"`) {
		t.Error("the form carries a station that is not installed")
	}
}

// A new revision asking for nothing that was not already accepted is applied
// on the spot. That is the whole point of re-fetching: fix the mod manager
// once, and every application running the station gets the fix.
func TestARefetchAskingForNothingNewIsApplied(t *testing.T) {
	s, database := catalogTestServer(t)
	served := testStation
	url := origin(t, &served)
	install(t, s, served, url)

	next := strings.Replace(testStation, `version: "1.0.0"`, `version: "1.1.0"`, 1)
	served = next

	if w := postStation(t, s.handleStationRefetch, only(t, database).ID); w.Code != http.StatusSeeOther {
		t.Fatalf("status %d, want a redirect:\n%s", w.Code, w.Body)
	}
	row := only(t, database)
	if row.YAML != next {
		t.Error("the new revision is not the one running")
	}
	if row.PrevYAML != testStation {
		t.Error("the revision it replaced was not kept")
	}
	if row.PendingYAML != "" {
		t.Error("a revision that asked for nothing new was held anyway")
	}
}

// The rule the whole model rests on. A station imported by URL is re-fetched by
// hand later; if a new revision could quietly widen what it reaches, the
// operator would be handing out a capability they never granted.
func TestARefetchThatAsksForMoreIsHeld(t *testing.T) {
	s, database := catalogTestServer(t)
	served := testStation
	url := origin(t, &served)
	install(t, s, served, url)

	greedy := strings.Replace(testStation,
		`allow: ["api.example.com"]`, `allow: ["api.example.com", "elsewhere.example.com"]`, 1)
	greedy = strings.Replace(greedy, `version: "1.0.0"`, `version: "2.0.0"`, 1)
	served = greedy

	if w := postStation(t, s.handleStationRefetch, only(t, database).ID); w.Code != http.StatusSeeOther {
		t.Fatalf("status %d, want a redirect:\n%s", w.Code, w.Body)
	}
	row := only(t, database)
	if row.YAML != testStation {
		t.Error("the running revision was replaced by one nobody accepted")
	}
	if row.PendingYAML != greedy {
		t.Error("the new revision was not kept for approval")
	}

	// And the page says what it would additionally be allowed to do, which is
	// the only part of it anybody needs to read.
	w := httptest.NewRequest("GET", "/settings/stations", nil)
	rec := httptest.NewRecorder()
	s.handleStations(rec, w)
	body := bodyText(rec)
	if !strings.Contains(body, "Revision 2.0.0 is waiting") || !strings.Contains(body, "elsewhere.example.com") {
		t.Errorf("the page does not say what is waiting or why:\n%s", body)
	}
}

func TestAcceptingAHeldRevisionRunsIt(t *testing.T) {
	s, database := catalogTestServer(t)
	served := testStation
	url := origin(t, &served)
	install(t, s, served, url)

	greedy := strings.Replace(testStation, `allow: ["api.example.com"]`, `allow: ["elsewhere.example.com"]`, 1)
	served = greedy
	postStation(t, s.handleStationRefetch, only(t, database).ID)

	if w := postStation(t, s.handleStationAccept, only(t, database).ID); w.Code != http.StatusSeeOther {
		t.Fatalf("status %d, want a redirect:\n%s", w.Code, w.Body)
	}
	row := only(t, database)
	if row.YAML != greedy || row.PendingYAML != "" {
		t.Error("accepting did not promote the waiting revision")
	}
	st, _ := station.Parse(greedy)
	if row.PermsHash != st.Permissions.Hash() {
		t.Error("what was accepted was not what got recorded")
	}
}

// Re-fetching an address that still serves the same document changes nothing —
// in particular it does not overwrite the revision to go back to.
func TestTheSameRevisionFetchedTwiceIsANoOp(t *testing.T) {
	s, database := catalogTestServer(t)
	served := testStation
	url := origin(t, &served)
	install(t, s, served, url)

	postStation(t, s.handleStationRefetch, only(t, database).ID)
	row := only(t, database)
	if row.PrevYAML != "" || row.PendingYAML != "" {
		t.Errorf("a re-fetch of the same document moved something: prev=%d pending=%d",
			len(row.PrevYAML), len(row.PendingYAML))
	}
}

// An update that breaks a panel is one click back, and the application it
// belongs to never stopped.
func TestRevertRestoresThePreviousDocument(t *testing.T) {
	s, database := catalogTestServer(t)
	served := testStation
	url := origin(t, &served)
	install(t, s, served, url)

	next := strings.Replace(testStation, `version: "1.0.0"`, `version: "1.1.0"`, 1)
	served = next
	postStation(t, s.handleStationRefetch, only(t, database).ID)

	if w := postStation(t, s.handleStationRevert, only(t, database).ID); w.Code != http.StatusSeeOther {
		t.Fatalf("status %d, want a redirect:\n%s", w.Code, w.Body)
	}
	row := only(t, database)
	if row.YAML != testStation {
		t.Error("reverting did not put the earlier revision back")
	}
	// And the revert is itself revertible: the one just left is where a second
	// click goes.
	if row.PrevYAML != next {
		t.Error("reverting threw away the revision it stepped off")
	}
}
