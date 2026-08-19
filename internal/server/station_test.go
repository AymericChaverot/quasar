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

// The fixtures and helpers every station test leans on: one document, and the
// few small routines for driving it through the two steps of an import.

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

// testStationWithParams is the same station with something to ask, which is
// what the install page is about.
var testStationWithParams = strings.Replace(testStation,
	"deploy:\n  deploy_type: compose",
	`deploy:
  app_name: "Demo {{SIZE}}"
  subdomain: "demo-{{SIZE}}"
  params:
    - {name: SIZE, label: How big, kind: select, default: small, options: [small, large]}
  env: |
    SIZE={{SIZE}}
  deploy_type: compose`, 1)

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
