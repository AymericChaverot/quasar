package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"quasar/internal/catalog"
	"quasar/internal/db"
	"quasar/internal/station"
)

// Deploying an application from an installed station: what the form asks, what
// the recap promises, and what the created application ends up carrying.

// Installing a station asks the station's own questions and nothing else.
// Everything the new-application form would have asked has already been
// answered by the document, and is behind Advanced options for the rare case
// where somebody wants to override one.
func TestTheInstallPageAsksOnlyWhatTheStationAsks(t *testing.T) {
	s, _ := catalogTestServer(t)
	install(t, s, testStationWithParams, "")

	r := httptest.NewRequest("GET", "/stations/demo/deploy", nil)
	r.SetPathValue("id", "demo")
	w := httptest.NewRecorder()
	s.handleStationDeployForm(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d, want the install page:\n%s", w.Code, w.Body)
	}

	body := bodyText(w)
	for _, want := range []string{
		"Demo asks",                            // the station's own questions
		`name="p.SIZE"`,                        // as fields named after them
		"What it is allowed to do",             // and what accepting it means
		"Run any command inside the container", // in the same words as the install screen
		"Advanced options",                     // everything else, folded away
		"Review and install",                   // and the recap between here and the deploy
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the install page does not have %q", want)
		}
	}

	// The machinery is out of the way rather than absent: an operator who
	// wants a second copy under another name can still say so.
	advanced := strings.Index(body, "Advanced options")
	if i := strings.Index(body, "nginx:alpine"); i < advanced {
		t.Error("the image is shown above the fold, where nobody needs it")
	}
}

// Between the form and the deploy is a recap. Installing takes an address on
// the operator's domain, writes an environment and starts pulling an image, and
// the person pressing the button is often the one least likely to know that;
// reading what is about to exist, with a way back to the answers that decided
// it, is what makes it a decision.
func TestTheRecapSaysWhatIsAboutToBeCreated(t *testing.T) {
	s, _ := catalogTestServer(t)
	install(t, s, testStationWithParams, "")

	form := strings.NewReader("p.SIZE=large")
	r := httptest.NewRequest("POST", "/stations/demo/deploy/review", form)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.SetPathValue("id", "demo")
	w := httptest.NewRecorder()
	s.handleStationDeployReview(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d, want the recap:\n%s", w.Code, w.Body)
	}

	body := bodyText(w)
	for _, want := range []string{
		"What this will create",
		"Demo large",  // the name it will take
		"demo-large.", // and the address, on this server's domain
		"What you answered",
		"large",                      // the answer that decided both
		`name="p.SIZE"`,              // carried forward, so the deploy has it
		"Install and deploy",         // the button that does it
		"/stations/demo/deploy/edit", // and the way back to change it
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the recap does not have %q:\n%s", want, body)
		}
	}

	// Nothing has happened: the recap is a screen, not a step.
	if apps, _ := db.ListApps(s.db, s.keyring); len(apps) != 0 {
		t.Errorf("reviewing a station created %d applications", len(apps))
	}
}

// The station's questions become the application, and the application keeps
// the answers because its script reads them back.
func TestInstallingAStationFillsTheApplicationFromItsDocument(t *testing.T) {
	s, _ := catalogTestServer(t)
	doc, err := station.Parse(testStationWithParams)
	if err != nil {
		t.Fatal(err)
	}

	tpl := doc.Template()
	values := tpl.Resolve(catalog.Values{"SIZE": "large"})
	app, kept := s.fillFrom(tpl, values)

	if app.Name != "Demo large" || app.Subdomain != "demo-large" {
		t.Errorf("the answers did not reach the name or the address: %+v", app)
	}
	if !strings.Contains(app.EnvContent, "SIZE=large") {
		t.Errorf("the answers did not reach the environment: %q", app.EnvContent)
	}
	if !strings.Contains(app.ComposeYAML, "nginx:alpine") || app.ComposeService != "app" {
		t.Errorf("the deploy block did not reach the application: %+v", app)
	}
	if kept != `{"SIZE":"large"}` {
		t.Errorf("the answers kept for the script are %s", kept)
	}
}

// A value the station never offered is not one an operator can pick, whatever
// they post: the answers are read back by the station's own script, and this is
// where that stops being a place to put arbitrary text.
func TestOnlyOfferedAnswersSurvive(t *testing.T) {
	s, _ := catalogTestServer(t)
	doc, _ := station.Parse(testStationWithParams)

	tpl := doc.Template()
	values := tpl.Resolve(catalog.Values{"SIZE": "enormous", "SOMETHING_ELSE": "x"})
	_, kept := s.fillFrom(tpl, values)

	if kept != `{"SIZE":"small"}` {
		t.Errorf("kept %s, want the declared default and nothing it did not offer", kept)
	}
}

// A station nobody installed has no install page, rather than an empty one.
func TestAnUnknownStationHasNoInstallPage(t *testing.T) {
	s, _ := catalogTestServer(t)

	r := httptest.NewRequest("GET", "/stations/nothing/deploy", nil)
	r.SetPathValue("id", "nothing")
	w := httptest.NewRecorder()
	s.handleStationDeployForm(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("status %d, want a 404", w.Code)
	}
}
