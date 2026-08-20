package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"quasar/internal/station"
)

// A parameter whose options come from the station's own script.
//
// The list nobody can write down — every release of a game there has ever been
// — is the case this exists for, and the two things worth holding it to are
// that the form offers what the station said, and that the deployment then
// accepts it. A form offering a value the deployment silently replaces with the
// default is worse than one that never offered it.

// testStationWithChoices asks one question whose answers the script supplies.
const testStationWithChoices = `
schema: 1
id: demo
name: Demo
description: A station whose form asks the script what to offer
version: "1.0.0"

deploy:
  deploy_type: compose
  compose_service: app
  port: 8080
  app_name: "Demo {{SIZE}}"
  subdomain: "demo-{{SIZE}}"
  params:
    - {name: SIZE, label: How big, kind: select, default: small,
       options: [small], options_from: sizes}
  env: |
    SIZE={{SIZE}}
  compose: |
    services:
      app:
        image: nginx:alpine

ui:
  tabs:
    - id: main
      name: Main
      panels:
        - {id: hello, type: stat, title: Hello, source: {action: hello}}

script: |
  export function sizes() { return { data: ['small', 'enormous'] } }
  export function hello() { return { data: { value: 1 } } }
`

func TestAParameterOffersWhatTheStationAnswered(t *testing.T) {
	s, _ := catalogTestServer(t)
	install(t, s, testStationWithChoices, "")

	// The answer as if it had just been asked for. Seeded rather than run,
	// because running it means spawning a worker out of a test binary; what is
	// under test here is what the form and the deployment do with the answer.
	s.choices.put("demo", "sizes", stationChoice{options: []string{"small", "enormous"}})

	r := httptest.NewRequest("GET", "/stations/demo/deploy", nil)
	r.SetPathValue("id", "demo")
	w := httptest.NewRecorder()
	s.handleStationDeployForm(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d:\n%s", w.Code, w.Body)
	}
	if body := bodyText(w); !strings.Contains(body, "enormous") {
		t.Error("the form does not offer what the station answered")
	}

	// And the deployment takes it. Resolve drops a value the parameter does
	// not accept, so a station whose script offered it and whose document did
	// not is exactly the case this has to get right.
	st, ok := s.station("demo")
	if !ok {
		t.Fatal("the station is not installed")
	}
	values := s.stationTemplate(context.Background(), r, st).Resolve(map[string]string{"SIZE": "enormous"})
	if values["SIZE"] != "enormous" {
		t.Errorf("SIZE = %q, want the value the form offered", values["SIZE"])
	}
}

// The answer leads, because it is the source speaking and it knows the order —
// newest first, for a list of releases. What the document wrote is never lost:
// it is what the form falls back to when nothing comes back, and the values in
// it the answer never mentioned are still offered.
func TestTheAnsweredOptionsLeadAndTheWrittenOnesSurvive(t *testing.T) {
	got := merge([]string{"1.21.9", "1.21.4"}, []string{"1.21.4", "LATEST"})
	if strings.Join(got, ",") != "1.21.9,1.21.4,LATEST" {
		t.Errorf("options = %v", got)
	}
	if got := merge(nil, []string{"small"}); strings.Join(got, ",") != "small" {
		t.Errorf("with no answer, options = %v", got)
	}
}

func TestAnOptionsActionReturnsAListOfValues(t *testing.T) {
	got, err := optionList(json.RawMessage(`["1.21.4", 20, "1.20.1"]`))
	if err != nil {
		t.Fatalf("a list of values was refused: %v", err)
	}
	if strings.Join(got.options, ",") != "1.21.4,20,1.20.1" {
		t.Errorf("options = %v", got.options)
	}
	if got.picked != "" {
		t.Errorf("a bare list named a default: %q", got.picked)
	}

	// The longer shape, for a source that also says which one to start on.
	full, err := optionList(json.RawMessage(`{"options":["1.21.9","1.21.4"],"default":"1.21.9"}`))
	if err != nil {
		t.Fatalf("an answer with a default was refused: %v", err)
	}
	if full.picked != "1.21.9" || strings.Join(full.options, ",") != "1.21.9,1.21.4" {
		t.Errorf("answer = %+v", full)
	}

	// Anything else is the author's mistake, and says so rather than drawing an
	// empty dropdown.
	for _, bad := range []string{`{"versions":["1.21.4"]}`, `[{"id":"1.21.4"}]`, `{"options":"1.21.4"}`, ``} {
		if _, err := optionList(json.RawMessage(bad)); err == nil {
			t.Errorf("%s was accepted as a list of options", bad)
		}
	}
}

// The form starts on what the station said is current, rather than on the
// version its document happened to be written around — and only where the
// station is also offering it, since a form proposing a value it would then
// refuse is worse than one proposing the document's.
func TestTheAnswerCanSayWhichOptionToStartOn(t *testing.T) {
	s, _ := catalogTestServer(t)
	install(t, s, testStationWithChoices, "")
	st, ok := s.station("demo")
	if !ok {
		t.Fatal("the station is not installed")
	}

	r := httptest.NewRequest("GET", "/stations/demo/deploy", nil)
	s.choices.put("demo", "sizes", stationChoice{options: []string{"enormous", "small"}, picked: "enormous"})
	if got := s.stationTemplate(r.Context(), r, st).Resolve(nil)["SIZE"]; got != "enormous" {
		t.Errorf("the form starts on %q, want the one the station named", got)
	}

	// A default nobody is offering is not a default.
	s.choices.put("demo", "sizes", stationChoice{options: []string{"small"}, picked: "colossal"})
	if got := s.stationTemplate(r.Context(), r, st).Resolve(nil)["SIZE"]; got != "small" {
		t.Errorf("the form starts on %q, want the document's own default", got)
	}
}

// An options action runs before the application exists, so there is nothing for
// it to act on. Reaching for one is refused in words its author can act on,
// rather than crashing on the application that is not there.
func TestAnInstallFormCallReachesNothingButTheNetwork(t *testing.T) {
	s := brokerTestServer(t)
	c := &stationCall{srv: s, doc: station.Station{ID: "demo", Name: "Demo",
		Permissions: station.Permissions{
			Exec:  station.Services{Services: []string{"app"}},
			Files: station.Files{Paths: []string{"data/**"}},
		}}}

	for _, capability := range []string{"files.read", "exec", "env.get", "store.get", "lifecycle"} {
		_, err := ask(t, c, capability, map[string]any{"path": "data/x", "key": "K", "service": "app"})
		if err == nil {
			t.Fatalf("%s was performed for a call with no application", capability)
		}
		if !strings.Contains(err.Error(), "no application yet") {
			t.Errorf("%s failed with %q, which does not say why", capability, err)
		}
	}
}
