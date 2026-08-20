package runtime_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"quasar/internal/station"
	"quasar/internal/station/ui"
	"quasar/internal/station/worker"
)

// stations/limits.yaml is a station that breaks itself in every way it can,
// and its whole value is that each way is the documented one. A page claiming
// "this comes back as a timeout an author can read" is worth nothing unless it
// does, so the claims are run rather than read.
//
// Everything here goes through the real worker: a separate process, bounded
// from outside, holding nothing.
func loadLimitsScript(t *testing.T) string {
	t.Helper()
	path := filepath.Join("..", "..", "..", "stations", "limits.yaml")
	doc, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	s, err := station.Parse(string(doc))
	if err != nil {
		t.Fatalf("%s no longer parses: %v", path, err)
	}
	return s.Script
}

func TestTheLimitsStationBreaksInTheWaysItSaysItDoes(t *testing.T) {
	script := loadLimitsScript(t)

	t.Run("a call that works", func(t *testing.T) {
		out, _, err := call(t, script, "fine")
		if err != nil {
			t.Fatalf("the one action that should work did not: %v", err)
		}
		if !strings.Contains(string(out.Value), `"ok"`) {
			t.Errorf("got %s", out.Value)
		}
	})

	t.Run("a loop that never ends", func(t *testing.T) {
		_, _, err := call(t, script, "never_returns", func(_ *worker.Call, lim *worker.Limits, _ *asked) {
			lim.Wall, lim.Grace = 500*time.Millisecond, 5*time.Second
		})
		var se *worker.ScriptError
		if !errors.As(err, &se) {
			t.Fatalf("error is %T (%v); the page says this comes back as something an author can read", err, err)
		}
		if !strings.Contains(se.Message, "longer than it is allowed") {
			t.Errorf("the message does not say what happened: %q", se.Message)
		}
	})

	t.Run("allocating without pause", func(t *testing.T) {
		_, _, err := call(t, script, "eats_memory", func(_ *worker.Call, lim *worker.Limits, _ *asked) {
			lim.Wall, lim.Grace = 30*time.Second, time.Second
			lim.MaxMemoryBytes = 160 << 20
		})
		var f *worker.Failure
		if !errors.As(err, &f) || f.Reason != worker.FailMemory {
			t.Fatalf("error is %v; the page says the parent kills it on the memory ceiling", err)
		}
	})

	t.Run("returning far too much", func(t *testing.T) {
		_, _, err := call(t, script, "returns_too_much", func(_ *worker.Call, lim *worker.Limits, _ *asked) {
			lim.Wall = 20 * time.Second
		})
		if err == nil {
			t.Fatal("twelve megabytes came through")
		}
	})

	t.Run("a script that throws", func(t *testing.T) {
		_, _, err := call(t, script, "explodes")
		if err == nil || !strings.Contains(err.Error(), "allowed to be wrong") {
			t.Errorf("error is %v, want the author's own words", err)
		}
	})

	t.Run("recursion with no bottom", func(t *testing.T) {
		_, _, err := call(t, script, "bottomless", func(_ *worker.Call, lim *worker.Limits, _ *asked) {
			lim.Wall, lim.Grace = 10*time.Second, 2*time.Second
		})
		if err == nil {
			t.Error("a bottomless recursion returned a value")
		}
	})

	// And the dashboard is still here, which is the claim all six are making.
	t.Run("and the next call still works", func(t *testing.T) {
		if _, _, err := call(t, script, "fine"); err != nil {
			t.Errorf("after all that, an ordinary call failed: %v", err)
		}
	})
}

// The page says a script sees nothing but quasar. It is a list on a panel, so
// it is a list that can be checked.
func TestTheLimitsStationSeesNothingButQuasar(t *testing.T) {
	out, _, err := call(t, loadLimitsScript(t), "what_is_there")
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Data []struct{ Key, Value string } `json:"data"`
	}
	if err := json.Unmarshal(out.Value, &result); err != nil {
		t.Fatalf("the panel's data is not readable: %s", out.Value)
	}
	if len(result.Data) == 0 {
		t.Fatal("the panel listed nothing")
	}
	for _, row := range result.Data {
		want := "undefined"
		if row.Key == "quasar" {
			want = "object"
		}
		if row.Value != want {
			t.Errorf("%s is %q in a station's script, want %q", row.Key, row.Value, want)
		}
	}
}

// And that a station granted nothing is refused everything, with the refusal
// naming what was missing rather than reading as an undefined.
func TestTheLimitsStationIsAllowedNothing(t *testing.T) {
	out, _, err := call(t, loadLimitsScript(t), "what_is_allowed")
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Data []struct{ Key, Value string } `json:"data"`
	}
	if err := json.Unmarshal(out.Value, &result); err != nil {
		t.Fatalf("the panel's data is not readable: %s", out.Value)
	}
	for _, row := range result.Data {
		if strings.Contains(row.Value, "ALLOWED") {
			t.Errorf("%s worked for a station granted nothing", row.Key)
		}
		if strings.Contains(row.Value, "undefined") {
			t.Errorf("%s reads as a missing object rather than a missing permission: %q", row.Key, row.Value)
		}
	}
}

// stations/components.yaml draws one of every component, and each one is only
// worth shipping if the action behind it returns the shape that component
// reads. A panel that renders as "this table's action returned a string" in an
// example is worse than no example.
//
// Two of its panels are wrong on purpose — that is what they are there to show
// — and they are named here rather than skipped by guesswork.
func TestTheComponentsStationFillsEveryPanel(t *testing.T) {
	path := filepath.Join("..", "..", "..", "stations", "components.yaml")
	doc, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s, err := station.Parse(string(doc))
	if err != nil {
		t.Fatalf("%s no longer parses: %v", path, err)
	}

	// The store is the only thing this station reaches, so a map is the whole
	// of the parent it needs.
	kept := map[string]json.RawMessage{}
	broker := &asked{answer: func(capability string, args json.RawMessage) (json.RawMessage, error) {
		var a struct {
			Key   string          `json:"key"`
			Value json.RawMessage `json:"value"`
		}
		json.Unmarshal(args, &a)
		switch capability {
		case "store.get":
			if v, ok := kept[a.Key]; ok {
				return v, nil
			}
			return json.RawMessage("null"), nil
		case "store.set":
			kept[a.Key] = a.Value
			return json.RawMessage("null"), nil
		case "store.delete":
			delete(kept, a.Key)
			return json.RawMessage("null"), nil
		case "store.keys":
			keys := []string{}
			for k := range kept {
				keys = append(keys, k)
			}
			return json.Marshal(keys)
		}
		return nil, errors.New("this station has not been granted " + capability)
	}}

	deliberatelyWrong := map[string]bool{"c_broken": true, "c_throws": true}
	for _, tab := range s.UI.Tabs {
		for _, panel := range allPanels(tab.Panels) {
			if panel.Source.Action == "" || deliberatelyWrong[panel.ID] {
				continue
			}
			t.Run(panel.ID, func(t *testing.T) {
				out, _, err := callWith(t, s.Script, panel.Source.Action, broker)
				if err != nil {
					t.Fatalf("%s: %v", panel.Source.Action, err)
				}
				result := ui.ParseResult(out.Value)
				if result.Error != "" {
					t.Fatalf("%s: %s", panel.Source.Action, result.Error)
				}
				if v := ui.Render("abcd1234", panel, result.Data); v.Problem != "" {
					t.Errorf("a %s panel got: %s", panel.Type, v.Problem)
				}
			})
		}
	}

	// And the two that are wrong are still wrong, because that is their job.
	for _, id := range []string{"c_broken", "c_throws"} {
		panel := findPanel(s, id)
		out, _, err := callWith(t, s.Script, panel.Source.Action, broker)
		if err != nil {
			continue // throwing is one of the two ways to be the example
		}
		if v := ui.Render("abcd1234", panel, ui.ParseResult(out.Value).Data); v.Problem == "" {
			t.Errorf("%s was supposed to show what a wrong shape looks like, and did not", id)
		}
	}
}

func allPanels(panels []ui.Panel) []ui.Panel {
	var out []ui.Panel
	for _, p := range panels {
		out = append(out, p)
		out = append(out, allPanels(p.Panels)...)
	}
	return out
}

func findPanel(s station.Station, id string) ui.Panel {
	for _, tab := range s.UI.Tabs {
		for _, p := range allPanels(tab.Panels) {
			if p.ID == id {
				return p
			}
		}
	}
	return ui.Panel{}
}

// Nothing survives between two calls, which was already the rule and is now
// enforced by the operating system rather than by convention.
func TestNothingSurvivesBetweenTwoCalls(t *testing.T) {
	script := loadLimitsScript(t)
	for i := 0; i < 3; i++ {
		out, _, err := call(t, script, "leftover")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(out.Value), `"value":1`) {
			t.Fatalf("call %d saw %s, want a process that had never run before", i+1, out.Value)
		}
	}
}

// Parse and Validate say a great deal about a station document and nothing at
// all about its script. A missing brace therefore installs cleanly and fails
// at the first click — on somebody else's server, the first time they tried
// the feature — which is the failure the folder full of examples can least
// afford.
//
// Loading it is the whole check, and the action asked for is missing on
// purpose: Run loads the script before it goes looking for one, so a script
// that is good JavaScript comes back complaining about the action and a script
// that is not comes back complaining about itself. Telling those two messages
// apart is the test.
func TestEveryShippedStationIsLoadableJavaScript(t *testing.T) {
	dir := filepath.Join("..", "..", "..", "stations")
	paths, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		t.Fatalf("looking for shipped stations: %v", err)
	}
	if len(paths) == 0 {
		t.Fatalf("no station in %s", dir)
	}

	for _, path := range paths {
		name := strings.TrimSuffix(filepath.Base(path), ".yaml")
		t.Run(name, func(t *testing.T) {
			doc, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("reading: %v", err)
			}
			s, err := station.Parse(string(doc))
			if err != nil {
				t.Fatalf("%s no longer parses: %v", path, err)
			}
			if strings.TrimSpace(s.Script) == "" {
				return // a station may legitimately carry none
			}

			_, _, err = call(t, s.Script, "there_is_no_action_by_this_name")
			if err == nil {
				t.Fatal("an action nothing exports came back with a value")
			}
			if !strings.Contains(err.Error(), "exports no action") {
				t.Errorf("the script would not load: %v", err)
			}
		})
	}
}

// The Minecraft station draws its own graph: it keeps a player count in its
// store and returns an SVG as a data: URI, which is the format's claim that a
// script never produces markup taken to its limit — a picture computed by a
// station, landing in an attribute, drawn by the browser, unable to reach the
// page around it.
//
// It is worth running rather than reading because three separate things have
// to hold for it to appear at all: the script has to build a URI, the image
// component has to accept one, and html/template has to keep it instead of
// writing #ZgotmplZ over it.
func TestTheMinecraftStationDrawsItsOwnGraph(t *testing.T) {
	path := filepath.Join("..", "..", "..", "stations", "minecraft.yaml")
	doc, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s, err := station.Parse(string(doc))
	if err != nil {
		t.Fatalf("%s no longer parses: %v", path, err)
	}

	// A day of samples is the only thing it is allowed to read.
	const samples = `[{"t":"09:00","n":0},{"t":"09:10","n":3},{"t":"09:20","n":7},{"t":"09:30","n":2}]`
	broker := &asked{answer: func(capability string, args json.RawMessage) (json.RawMessage, error) {
		if capability != "store.get" {
			return nil, errors.New("this station has not been granted " + capability)
		}
		var a struct {
			Key string `json:"key"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, err
		}
		if a.Key == "samples" {
			return json.RawMessage(samples), nil
		}
		return json.RawMessage("null"), nil
	}}

	out, _, err := callWith(t, s.Script, "players_graph", broker)
	if err != nil {
		t.Fatalf("the graph did not come back: %v", err)
	}
	result := ui.ParseResult(out.Value)
	if result.Error != "" {
		t.Fatalf("the graph reported: %s", result.Error)
	}
	if result.Waiting != "" {
		t.Fatalf("four samples was not enough for it: %s", result.Waiting)
	}

	view := ui.Render("abcd1234", findPanel(s, "mc_graph"), result.Data)
	if view.Problem != "" {
		t.Fatalf("the image panel got: %s", view.Problem)
	}
	src := string(view.ImageSrc())
	if !strings.HasPrefix(src, "data:image/svg+xml") {
		t.Fatalf("the panel would draw %.80q", src)
	}
	if !strings.Contains(src, "%3Csvg") {
		t.Errorf("the SVG did not survive being made into a URI: %.120q", src)
	}
}

// The version dropdowns, run.
//
// Three things have to agree for the Settings tab to offer every release
// Mojang has: the script has to read the manifest, it has to hand the form a
// value and a list rather than a value, and the form has to prefer that list to
// the one the document wrote. None of the three is visible from reading the
// YAML, and the failure if any of them is wrong is a dropdown holding one
// version — which looks like a server with nothing to upgrade to.
func TestTheMinecraftStationOffersEveryRelease(t *testing.T) {
	s := loadStation(t, "minecraft.yaml")

	const manifest = `{"latest":{"release":"1.21.4"},"versions":[
		{"id":"25w03a","type":"snapshot"},
		{"id":"1.21.4","type":"release"},
		{"id":"1.21.1","type":"release"},
		{"id":"b1.7.3","type":"old_beta"}]}`

	broker := &asked{answer: func(capability string, args json.RawMessage) (json.RawMessage, error) {
		switch capability {
		case "http.get":
			if !strings.Contains(string(args), "piston-meta.mojang.com") {
				return nil, errors.New("this station has not been granted that host")
			}
			return json.Marshal(map[string]any{"status": 200, "body": manifest})
		case "env.get":
			return json.Marshal("1.21.1")
		}
		return nil, errors.New("this station has not been granted " + capability)
	}}

	// What the install form asks for, before there is an application at all.
	out, _, err := callWith(t, s.Script, "official_versions", broker)
	if err != nil {
		t.Fatalf("the install form's options did not come back: %v", err)
	}
	if got := string(ui.ParseResult(out.Value).Data); got != `["1.21.4","1.21.1"]` {
		t.Errorf("it offers %s, want the releases and only the releases", got)
	}

	// And the same list on the Settings tab, with this server's own version
	// selected — which is the half the renderer has to agree about.
	out, _, err = callWith(t, s.Script, "version_form", broker)
	if err != nil {
		t.Fatalf("the version form did not come back: %v", err)
	}
	view := ui.Render("abcd1234", findPanel(s, "mc_version"), ui.ParseResult(out.Value).Data)
	if view.Problem != "" {
		t.Fatalf("the form got: %s", view.Problem)
	}
	field := view.Fields[0]
	if field.Value != "1.21.1" {
		t.Errorf("the version selected is %q, want the one this server runs", field.Value)
	}
	if strings.Join(field.Options, ",") != "1.21.4,1.21.1" {
		t.Errorf("the dropdown holds %v", field.Options)
	}
}

// loadStation parses one of the shipped documents.
func loadStation(t *testing.T, name string) station.Station {
	t.Helper()
	path := filepath.Join("..", "..", "..", "stations", name)
	doc, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s, err := station.Parse(string(doc))
	if err != nil {
		t.Fatalf("%s no longer parses: %v", path, err)
	}
	return s
}
