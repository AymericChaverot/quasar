package server

import (
	"bytes"
	"encoding/json"
	"html"
	"strings"
	"testing"

	"quasar/internal/db"
	"quasar/internal/station"
	"quasar/internal/station/ui"
)

// renderPanelPartial draws one panel the way the page does, so a test can
// assert on what an operator would actually see rather than on a struct.
func renderPanelPartial(t *testing.T, v ui.PanelView) string {
	t.Helper()
	s := testServer(t)
	var buf bytes.Buffer
	if err := s.pages["app_detail"].ExecuteTemplate(&buf, "station_panel", v); err != nil {
		t.Fatalf("rendering the panel: %v", err)
	}
	return html.UnescapeString(buf.String())
}

var testModTable = ui.Panel{
	ID: "mod_list", Type: "table", Title: "Installed mods",
	Empty: "No mods installed yet.",
	Columns: []ui.Column{
		{Key: "name", Label: "Mod"},
		{Key: "version", Label: "Version", Align: "right"},
	},
	RowActions: []ui.Action{
		{Label: "Remove", Action: "remove_mod", Tone: "err", Confirm: "Remove {{name}}?"},
	},
}

func TestATableDrawsItsRowsAndItsActions(t *testing.T) {
	v := ui.Render("abcd1234", testModTable, json.RawMessage(
		`[{"name":"Sodium","version":"0.5.8","file":"sodium-0.5.8.jar"}]`))

	page := renderPanelPartial(t, v)
	for _, want := range []string{
		"Installed mods",  // the panel's own title
		"Sodium", "0.5.8", // the row
		"/apps/abcd1234/station/action/remove_mod", // where the button posts
		"Remove Sodium?",   // the question, about this row
		"sodium-0.5.8.jar", // the whole row travels with it
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the table does not draw %q", want)
		}
	}
}

// An empty table draws the sentence the document wrote, not a blank card.
func TestAnEmptyTableDrawsItsSentence(t *testing.T) {
	page := renderPanelPartial(t, ui.Render("abcd1234", testModTable, json.RawMessage(`[]`)))

	if !strings.Contains(page, "No mods installed yet.") {
		t.Errorf("the empty state is missing:\n%s", page)
	}
	if strings.Contains(page, "<table") {
		t.Error("an empty table drew a table")
	}
}

// The test the whole file exists for. An author whose panel renders blank gives
// up; one who reads what their action returned and what the component needed
// fixes it.
func TestAWrongShapeDrawsALegibleError(t *testing.T) {
	page := renderPanelPartial(t, ui.Render("abcd1234", testModTable, json.RawMessage(`"three mods"`)))

	if !strings.Contains(page, "nothing to show") {
		t.Errorf("the panel does not say it has nothing to draw:\n%s", page)
	}
	for _, want := range []string{"a string", "list of rows"} {
		if !strings.Contains(page, want) {
			t.Errorf("the panel does not say %q", want)
		}
	}
}

// The panel listens for one event and the action's response sends the same one.
// They are written in two files, so the thing worth testing is that they agree.
func TestAnActionsRefreshReachesTheRightPanel(t *testing.T) {
	block := &StationBlock{
		App: &db.App{ID: "abcd1234"},
		Doc: station.Station{
			Name: "Demo",
			UI: ui.UI{Tabs: []ui.Tab{{ID: "mods", Name: "Mods", Panels: []ui.Panel{
				testModTable,
				{ID: "add", Type: "form", Fields: []ui.Field{{Name: "url"}},
					Submit: ui.Action{Label: "Install", Action: "add_mod"}},
			}}}},
		},
	}

	s := testServer(t)
	var buf bytes.Buffer
	if err := s.pages["app_detail"].ExecuteTemplate(&buf, "station_block", block); err != nil {
		t.Fatalf("rendering the block: %v", err)
	}
	page := html.UnescapeString(buf.String())

	event := stationRefreshEvent("mod_list")
	if !strings.Contains(page, event+" from:body") {
		t.Errorf("the mod list does not listen for %q:\n%s", event, page)
	}

	// What an action returning refresh: [mod_list] sends back.
	sent := refreshEvents(ui.Result{Toast: "installed", Refresh: []string{"mod_list"}})
	if sent != event {
		t.Errorf("an action asks for %q and the panel listens for %q", sent, event)
	}
	// And only that panel: the form beside it should not flicker.
	if strings.Contains(sent, "add") {
		t.Errorf("the refresh reaches panels nobody named: %q", sent)
	}
}

// A station's script exports helpers as well as actions, and the name of the
// one to run arrives in a URL. Only what the interface actually reaches may be
// run.
func TestOnlyActionsTheInterfaceReachesCanBeRun(t *testing.T) {
	doc := station.Station{
		UI: ui.UI{Tabs: []ui.Tab{{ID: "t", Name: "T", Panels: []ui.Panel{
			{ID: "p", Type: "stat", Source: ui.Source{Action: "player_count"}},
		}}}},
		Script: "export function player_count() {}\nexport function rconExec() {}\n",
	}

	if !stationOffers(doc, "player_count") {
		t.Error("an action a panel names cannot be run")
	}
	if stationOffers(doc, "rconExec") {
		t.Error("a helper the interface never reaches can be run from a URL")
	}
	if stationOffers(doc, "") {
		t.Error("an empty action name was accepted")
	}
}

// The tab strip is the station's own, and it stops at the block: the
// navigation, the top bar and every section below keep the operator's page.
func TestTheBlockDrawsItsTabStrip(t *testing.T) {
	block := &StationBlock{
		App: &db.App{ID: "abcd1234"},
		Doc: station.Station{
			Name: "Minecraft", Description: "Fabric or Forge", Version: "1.2.0",
			UI: ui.UI{Tabs: []ui.Tab{
				{ID: "overview", Name: "Server", Panels: []ui.Panel{{ID: "players", Type: "stat"}}},
				{ID: "mods", Name: "Mods", Panels: []ui.Panel{testModTable}},
			}},
		},
	}

	s := testServer(t)
	var buf bytes.Buffer
	if err := s.pages["app_detail"].ExecuteTemplate(&buf, "station_block", block); err != nil {
		t.Fatal(err)
	}
	page := html.UnescapeString(buf.String())

	for _, want := range []string{"Minecraft", "1.2.0", `data-station-tab="overview"`, `data-station-tab="mods"`, "Server", "Mods"} {
		if !strings.Contains(page, want) {
			t.Errorf("the block does not draw %q", want)
		}
	}
	// The first tab is the one open, and the second is not.
	if !strings.Contains(page, `data-station-pane="mods"`) || !strings.Contains(page, "hidden") {
		t.Error("the panes are not drawn one at a time")
	}
}
