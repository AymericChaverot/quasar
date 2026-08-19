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

// Every component the format offers draws something. A type a document may
// declare and the page cannot draw is a blank card with no explanation, which
// is the one failure this whole file exists to prevent.
func TestEveryComponentDraws(t *testing.T) {
	// Enough of a panel for each type to have what it reads, and data of the
	// shape its own renderer asks for.
	data := map[string]string{
		"table": `[{"name":"Sodium","version":"0.5.8"}]`,
		"stat":  `{"value":3,"suffix":"/ 20"}`,
		"gauge": `{"value":42,"label":"disk"}`,
		"list":  `["one","two"]`, "timeline": `[{"label":"deployed","note":"2m ago"}]`,
		"keyvalue": `{"Loader":"FABRIC"}`,
		"markdown": `"Some words."`, "code": `"a = 1"`, "banner": `"Heads up."`,
		"image": `"data:image/svg+xml;base64,PHN2Zy8+"`,
	}

	for _, kind := range ui.PanelTypes() {
		panel := ui.Panel{ID: "p", Type: kind, Title: "A panel", Label: "Do it",
			Action: "go", Confirm: "Sure?", Src: "{{service:app:8080}}", Service: "app",
			Columns: []ui.Column{{Key: "name", Label: "Mod"}},
			Fields:  []ui.Field{{Name: "url", Label: "URL"}},
			Submit:  ui.Action{Label: "Send", Action: "go"},
			Panels:  []ui.Panel{{ID: "inner", Type: "stat"}},
		}
		v := ui.Render("abcd1234", panel, json.RawMessage(data[kind]))
		if kind == "log" {
			v = ui.Streaming("abcd1234", panel, "/apps/abcd1234/containers/app/logs")
		}
		if kind == "iframe" {
			v = ui.Embedded("abcd1234", panel, "/apps/abcd1234/station/embed/p/")
		}

		page := renderPanelPartial(t, v)
		if strings.Contains(page, "cannot draw") {
			t.Errorf("%s: the page has no component for it", kind)
		}
		if v.Problem != "" {
			t.Errorf("%s: %s", kind, v.Problem)
		}
	}
}

// A message floats over the page rather than sitting at the top of the block:
// the button that caused it is often three screens down from there, and a
// message nobody scrolls back up to read is a message nobody reads.
func TestAMessageFloatsAndCanBeDismissed(t *testing.T) {
	s := testServer(t)
	draw := func(result ui.Result) string {
		t.Helper()
		var buf bytes.Buffer
		if err := s.pages["app_detail"].ExecuteTemplate(&buf, "station_message", result); err != nil {
			t.Fatal(err)
		}
		return html.UnescapeString(buf.String())
	}

	toast := draw(ui.Result{Toast: "Sodium installed"})
	if !strings.Contains(toast, "station-toast") || !strings.Contains(toast, "Sodium installed") {
		t.Errorf("a toast does not render:\n%s", toast)
	}
	// It goes on its own, because it is a transient message and carries the
	// lifetime that says so. The block's script is what reads it: a fragment
	// that is appended rather than swapped cannot carry a script of its own
	// without leaving one behind per button anybody ever pressed.
	if !strings.Contains(toast, "data-toast-life") {
		t.Error("a toast stays on the page for ever")
	}

	// The middle case now has somewhere to go: neither a success worth a tick
	// nor a failure, and reporting it as either is a lie the operator acts on.
	warned := draw(ui.Result{Warn: "installed, but untested on this version"})
	if !strings.Contains(warned, "station-toast-warn") || !strings.Contains(warned, "untested") {
		t.Errorf("a warning does not render:\n%s", warned)
	}
	if !strings.Contains(warned, "data-toast-life") {
		t.Error("a warning stays on the page for ever")
	}

	// An error does not: the reason something failed is what somebody came to
	// read, and it waits until they have.
	failed := draw(ui.Result{Error: "no build of sodium for 1.20.1"})
	if !strings.Contains(failed, "no build of sodium") {
		t.Errorf("an error does not render:\n%s", failed)
	}
	if strings.Contains(failed, "data-toast-life") {
		t.Error("an error takes itself off the page")
	}
	if !strings.Contains(failed, "Dismiss") {
		t.Error("an error cannot be dismissed, so it is there for ever")
	}

	// Each says which of the three it is in a shape as well as a colour, for
	// everybody who cannot tell this green from that red.
	for _, c := range []string{toast, warned, failed} {
		if !strings.Contains(c, "station-toast-icon") {
			t.Errorf("this message says which it is in colour alone:\n%s", c)
		}
	}
}

// An application deployed from a station is not the page an ordinary
// application is. What somebody installed a station for is its own controls and
// its own status; build, routing, storage and the rest are still true and still
// needed, and they fold away rather than sitting between the station and the
// bottom of the page.
func TestAStationsApplicationFoldsAwayTheMachinery(t *testing.T) {
	s := testServer(t)
	app := AppView{App: &db.App{ID: "abcd1234", Name: "Server", Subdomain: "server", StationID: "demo"}}
	block := &StationBlock{
		App: app.App,
		Doc: station.Station{Name: "Demo", UI: ui.UI{Tabs: []ui.Tab{
			{ID: "t", Name: "T", Panels: []ui.Panel{{ID: "p", Type: "stat"}}},
		}}},
	}

	draw := func(data map[string]any) string {
		t.Helper()
		var buf bytes.Buffer
		if err := s.pages["app_detail"].ExecuteTemplate(&buf, "layout", data); err != nil {
			t.Fatal(err)
		}
		return html.UnescapeString(buf.String())
	}

	page := draw(map[string]any{"Title": "Server", "App": app, "IsAdmin": true, "Station": block})
	if !strings.Contains(page, "Advanced options") || !strings.Contains(page, "advanced-body") {
		t.Errorf("a station's application still leads with the machinery:\n%s", page)
	}
	// Folded, not removed: an admin who needs the environment still has it.
	if !strings.Contains(page, "Deploy webhook") {
		t.Error("folding the machinery away threw it away")
	}
	// The status bar and the station itself stay where they are.
	if !strings.Contains(page, `id="status-panel"`) || !strings.Contains(page, `id="station"`) {
		t.Error("the two things the page is for are inside the fold")
	}

	// An ordinary application is untouched: there is nothing to fold, because
	// the machinery is the whole page.
	plain := draw(map[string]any{"Title": "Blog", "App": AppView{App: &db.App{ID: "b", Name: "Blog"}}, "IsAdmin": true})
	if strings.Contains(plain, "Advanced options") {
		t.Error("an ordinary application hides its own page behind a disclosure")
	}
}

// The two or three stations somebody deploys weekly are the same two or three
// every week, and a starred one is lifted to its own list at the top — without
// disappearing from the list of everything, which has to mean everything.
func TestStarringAStationLiftsItWithoutLosingIt(t *testing.T) {
	s := testServer(t)
	demo := StationCard{Station: station.Station{ID: "demo", Name: "Demo", Version: "1.0.0"}}
	other := StationCard{Station: station.Station{ID: "other", Name: "Other", Version: "1.0.0"},
		Favorite: true}

	var buf bytes.Buffer
	err := s.pages["stations"].ExecuteTemplate(&buf, "stations_lists", map[string]any{
		"Stations":  []StationCard{demo, other},
		"Favorites": []StationCard{other},
		"IsAdmin":   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	page := html.UnescapeString(buf.String())

	for _, want := range []string{
		"Favourites (1)", // both lists carry their count
		"All stations (2)",
		"is-favorite",              // the starred card is marked as one
		"/stations/demo/favorite",  // and every card can be starred
		"/stations/other/favorite", // or unstarred
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the lists do not draw %q:\n%s", want, page)
		}
	}
	// Starred and still listed under everything: a list that quietly lost
	// entries as they were starred is one nobody would trust.
	if n := strings.Count(page, "/stations/other/deploy"); n != 2 {
		t.Errorf("the starred station appears %d times, want 2 (favourites and all)", n)
	}

	// A viewer cannot change what is installed, so they are not offered a star
	// that would only answer 403.
	buf.Reset()
	if err := s.pages["stations"].ExecuteTemplate(&buf, "stations_lists", map[string]any{
		"Stations": []StationCard{demo}, "IsAdmin": false,
	}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "station-star") {
		t.Error("a viewer is offered a star they cannot set")
	}
}

// The block refreshes itself. Reloading the page throws away the tab somebody
// was on and the place they had scrolled to, which is a lot to pay for one
// number — and the author's name belongs at the foot of their own work.
func TestTheBlockRefreshesItselfAndCreditsItsAuthor(t *testing.T) {
	s := testServer(t)
	block := &StationBlock{
		App: &db.App{ID: "abcd1234"},
		Doc: station.Station{Name: "Demo", Author: "Jean Dupont", UI: ui.UI{Tabs: []ui.Tab{
			{ID: "t", Name: "T", Panels: []ui.Panel{{ID: "p", Type: "stat"}}},
		}}},
	}
	var buf bytes.Buffer
	if err := s.pages["app_detail"].ExecuteTemplate(&buf, "station_block", block); err != nil {
		t.Fatal(err)
	}
	page := html.UnescapeString(buf.String())

	if !strings.Contains(page, "station-refresh") {
		t.Error("the only way to refresh a station is to reload the whole page")
	}
	// Every panel hears the one event, so the button needs no list of them.
	if !strings.Contains(page, stationRefreshAllEvent()+" from:body") {
		t.Errorf("panels do not listen for the station's own refresh:\n%s", page)
	}
	if !strings.Contains(page, "Station Author: Jean Dupont") {
		t.Errorf("the station does not credit whoever wrote it:\n%s", page)
	}
}

// A panel that has nothing to draw yet is not a panel that failed, and drawing
// it as one tells whoever pressed Deploy a moment ago that they broke it. It
// spins, and it asks again, so it connects itself when the container arrives.
func TestAWaitingPanelSpinsAndAsksAgain(t *testing.T) {
	panel := ui.Panel{ID: "output", Type: "log", Title: "Live output", Service: "minecraft"}
	page := renderPanelPartial(t, ui.Waiting("abcd1234", panel, "Waiting for Server to start"))

	if strings.Contains(page, "alert-err") {
		t.Errorf("waiting for a container is drawn as a failure:\n%s", page)
	}
	for _, want := range []string{
		"Waiting for Server to start",
		"spinner",
		"/apps/abcd1234/station/panel/output", // it asks again on its own
		"delay:2s",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("a waiting panel does not draw %q:\n%s", want, page)
		}
	}

	// A panel that really did fail still says so, and does not poll: an
	// author's bug hidden behind a spinner that never stops is worse than the
	// red card it replaced.
	failed := renderPanelPartial(t, ui.Failed("abcd1234", panel, "no such service"))
	if !strings.Contains(failed, "no such service") || strings.Contains(failed, "delay:2s") {
		t.Errorf("a real failure is hidden behind a spinner:\n%s", failed)
	}
}

// A script is the only thing that knows its server's process is up and its port
// is not answering yet, so it has a way to say exactly that.
func TestAScriptCanSayItIsNotReadyYet(t *testing.T) {
	r := ui.ParseResult(json.RawMessage(`{"waiting":"Waiting for the server to finish starting"}`))
	if r.Waiting != "Waiting for the server to finish starting" {
		t.Errorf("a script saying it is not ready is read as %+v", r)
	}
	if r.Error != "" {
		t.Error("not ready yet counts as an error")
	}
}

// Several actions are several things worth knowing. A message that overwrote
// the one before it would make both pointless, so every button appends and the
// block gives each arrival its lifetime as it lands.
func TestMessagesStackRatherThanOverwrite(t *testing.T) {
	s := testServer(t)
	v := ui.Render("abcd1234", ui.Panel{ID: "p", Type: "button", Label: "Go", Action: "go"}, nil)
	page := html.UnescapeString(renderPanelPartial(t, v))
	if !strings.Contains(page, `hx-swap="beforeend"`) {
		t.Errorf("a button's message crushes whatever was already there:\n%s", page)
	}

	block := &StationBlock{
		App: &db.App{ID: "abcd1234"},
		Doc: station.Station{Name: "Demo", UI: ui.UI{Tabs: []ui.Tab{
			{ID: "t", Name: "T", Panels: []ui.Panel{{ID: "p", Type: "stat"}}},
		}}},
	}
	var buf bytes.Buffer
	if err := s.pages["app_detail"].ExecuteTemplate(&buf, "station_block", block); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "toastLife") {
		t.Error("nothing arms the messages that land, so none of them ever go")
	}
}

// The block has two places for what comes back, and they are not the same
// place: a toast floats, a long action's progress pane stays on the page where
// it can be watched.
func TestTheBlockKeepsToastsAndJobPanesApart(t *testing.T) {
	block := &StationBlock{
		App: &db.App{ID: "abcd1234"},
		Doc: station.Station{Name: "Demo", UI: ui.UI{Tabs: []ui.Tab{
			{ID: "t", Name: "T", Panels: []ui.Panel{{ID: "p", Type: "stat"}}},
		}}},
	}

	s := testServer(t)
	var buf bytes.Buffer
	if err := s.pages["app_detail"].ExecuteTemplate(&buf, "station_block", block); err != nil {
		t.Fatal(err)
	}
	page := buf.String()

	if !strings.Contains(page, `id="station-message" class="station-toasts"`) {
		t.Error("there is nowhere for a message to float")
	}
	if !strings.Contains(page, `id="station-jobs"`) {
		t.Error("there is nowhere for a long action's pane to sit")
	}
	// The pane is inside the block, above the tabs; the toasts are not.
	if strings.Index(page, `id="station-jobs"`) > strings.Index(page, "</section>") {
		t.Error("the job pane is outside the block")
	}
	if strings.Index(page, `id="station-message"`) < strings.Index(page, "</section>") {
		t.Error("the toasts are inside the block, where they would scroll away")
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
