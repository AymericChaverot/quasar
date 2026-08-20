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
	s.choices.put("demo", "sizes", []string{"small", "enormous"})

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

// What the document wrote is never lost: it is what the form falls back to when
// nothing comes back, and it comes first because its author put it first.
func TestWrittenOptionsComeFirstAndSurvive(t *testing.T) {
	got := merge([]string{"small", "large"}, []string{"large", "enormous"})
	if strings.Join(got, ",") != "small,large,enormous" {
		t.Errorf("options = %v", got)
	}
	if got := merge([]string{"small"}, nil); strings.Join(got, ",") != "small" {
		t.Errorf("with no answer, options = %v", got)
	}
}

func TestAnOptionsActionReturnsAListOfValues(t *testing.T) {
	got, err := optionList(json.RawMessage(`["1.21.4", 20, "1.20.1"]`))
	if err != nil {
		t.Fatalf("a list of values was refused: %v", err)
	}
	if strings.Join(got, ",") != "1.21.4,20,1.20.1" {
		t.Errorf("options = %v", got)
	}

	// Anything else is the author's mistake, and says so rather than drawing an
	// empty dropdown.
	for _, bad := range []string{`{"versions":["1.21.4"]}`, `[{"id":"1.21.4"}]`, ``} {
		if _, err := optionList(json.RawMessage(bad)); err == nil {
			t.Errorf("%s was accepted as a list of options", bad)
		}
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
