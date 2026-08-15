package server

import (
	"database/sql"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"quasar/internal/catalog"
	"quasar/internal/db"
	"quasar/internal/secrets"
)

// catalogTestServer is a server with a real database and the real templates,
// which is what these need: the interesting behaviour is a document being
// accepted or handed back with a list of what is wrong with it, and the second
// half of that only exists on the rendered page.
func catalogTestServer(t *testing.T) (*Server, *sql.DB) {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	s := &Server{db: database, pages: map[string]*template.Template{}, guards: map[string]string{}, mux: http.NewServeMux()}
	if err := s.parseTemplates(); err != nil {
		t.Fatal(err)
	}
	return s, database
}

func post(t *testing.T, h http.HandlerFunc, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h(w, r)
	return w
}

func TestCatalogCreateStoresAValidDocument(t *testing.T) {
	s, database := catalogTestServer(t)

	w := post(t, s.handleCatalogCreate, "/settings/catalogs", url.Values{
		"name": {"My servers"}, "yaml": {catalog.Example},
	})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status %d, want a redirect — the document was rejected:\n%s", w.Code, w.Body)
	}

	rows, err := db.ListCatalogs(database)
	if err != nil || len(rows) != 1 {
		t.Fatalf("%d catalogues stored (%v), want 1", len(rows), err)
	}
	if !rows[0].Enabled {
		t.Error("a catalogue was stored disabled")
	}
	// The example names itself, and that name is the one every entry it
	// supplies is labelled with — so it is the one stored, not the one typed
	// beside it. Two names for one catalogue is one too many.
	if rows[0].Name != "My catalogue" {
		t.Errorf("name = %q, want the one the document declares", rows[0].Name)
	}
}

// The document is what the operator spent their time on. A rejected one comes
// back with the reasons and the text intact, not as a redirect to an empty box.
func TestCatalogCreateHandsBackWhatIsWrong(t *testing.T) {
	s, database := catalogTestServer(t)

	const broken = "entries:\n  - {id: mc, name: Mine, category: Games, port: 25565}\n"
	w := post(t, s.handleCatalogCreate, "/settings/catalogs", url.Values{
		"name": {"Mine"}, "yaml": {broken},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status %d, want the page back with the errors on it", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "description") {
		t.Error("the page does not say what is wrong with the document")
	}
	if !strings.Contains(body, "id: mc") {
		t.Error("the submitted document was not handed back")
	}

	if rows, _ := db.ListCatalogs(database); len(rows) != 0 {
		t.Errorf("%d catalogues stored, want none — a document that fails its checks must not be saved", len(rows))
	}
}

func TestCatalogCreateRejectsWhatWillNotParse(t *testing.T) {
	s, database := catalogTestServer(t)

	w := post(t, s.handleCatalogCreate, "/settings/catalogs", url.Values{
		"name": {"Mine"}, "yaml": {"entries:\n  - id: mc\n    deploytype: compose\n"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status %d, want the page back with the error on it", w.Code)
	}
	if rows, _ := db.ListCatalogs(database); len(rows) != 0 {
		t.Error("a document that does not parse was stored")
	}
}

// The point of the whole thing: what is stored has to reach the catalogue the
// new-application page is built from, and an entry reusing a built-in id has to
// replace it rather than appear beside it.
func TestStoredCatalogueReachesTheCatalogue(t *testing.T) {
	s, database := catalogTestServer(t)

	const doc = `
name: Mine
categories: [Modded]
entries:
  - id: minecraft
    name: My Minecraft
    description: The one I actually run
    category: Modded
    port: 25565
    image_ref: itzg/minecraft-server:latest
`
	if _, err := db.InsertCatalog(database, &db.Catalog{Name: "Mine", YAML: doc, Enabled: true}); err != nil {
		t.Fatal(err)
	}

	cat := s.catalog()
	e := cat.Get("minecraft")
	if e == nil {
		t.Fatal("the entry went missing from the merged catalogue")
	}
	if e.Name != "My Minecraft" {
		t.Errorf("entry is %q, want the operator's override", e.Name)
	}
	if n, want := len(cat.Templates), len(catalog.Builtin().Templates); n != want {
		t.Errorf("%d entries after the override, want %d — it replaced one, it did not add one", n, want)
	}
	var seen bool
	for _, g := range cat.Grouped() {
		if g.Category == "Modded" {
			seen = true
		}
	}
	if !seen {
		t.Error("the operator's category never reached the grouped catalogue")
	}
}

// A disabled catalogue stays stored and stops being offered. That is the
// difference between it and deleting one, and the reason the toggle exists.
func TestDisabledCatalogueIsNotOffered(t *testing.T) {
	s, database := catalogTestServer(t)

	id, err := db.InsertCatalog(database, &db.Catalog{Name: "Mine", YAML: catalog.Example, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	before := len(s.catalog().Templates)

	if err := db.SetCatalogEnabled(database, id, false); err != nil {
		t.Fatal(err)
	}
	if after := len(s.catalog().Templates); after != before-1 {
		t.Errorf("%d entries with it disabled, want %d", after, before-1)
	}
	if rows, _ := db.ListCatalogs(database); len(rows) != 1 {
		t.Error("disabling a catalogue deleted it")
	}
}

// A stored document that no longer reads must not take the page down with it.
// The catalogue it holds is left out, everything else still loads, and the
// catalogues page is where it says so.
func TestABrokenCatalogueDoesNotBreakThePage(t *testing.T) {
	s, database := catalogTestServer(t)
	if _, err := database.Exec(
		"INSERT INTO catalogs (name, yaml, enabled) VALUES (?, ?, 1)", "Broken", "entries: [oh"); err != nil {
		t.Fatal(err)
	}

	if n, want := len(s.catalog().Templates), len(catalog.Builtin().Templates); n != want {
		t.Errorf("%d entries, want the %d built-in ones", n, want)
	}
	views := s.catalogViews()
	if len(views) != 1 || len(views[0].Problems) == 0 {
		t.Fatalf("the page does not report the broken catalogue: %+v", views)
	}
}

func TestFetchOnlyTakesHTTPURLs(t *testing.T) {
	for _, raw := range []string{"file:///etc/passwd", "ftp://example.com/c.yaml", "not a url", ""} {
		if _, err := fetchCatalog(raw); err == nil {
			t.Errorf("%q was accepted", raw)
		}
	}
}

func TestFetchImportsADocument(t *testing.T) {
	s, database := catalogTestServer(t)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(catalog.Example))
	}))
	defer origin.Close()

	w := post(t, s.handleCatalogFetch, "/settings/catalogs/fetch", url.Values{
		"source_url": {origin.URL + "/catalogue.yaml"},
	})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status %d, want a redirect:\n%s", w.Code, w.Body)
	}
	rows, _ := db.ListCatalogs(database)
	if len(rows) != 1 {
		t.Fatalf("%d catalogues stored, want 1", len(rows))
	}
	if rows[0].SourceURL == "" {
		t.Error("the address it came from was not recorded, so it can never be re-fetched")
	}
	// The document names itself, and that name wins over the file name.
	if rows[0].Name != "My catalogue" {
		t.Errorf("name = %q, want the one the document declares", rows[0].Name)
	}
}

func TestFetchRefusesSomethingTooLarge(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(make([]byte, maxCatalogBytes+1))
	}))
	defer origin.Close()

	if _, err := fetchCatalog(origin.URL); err == nil {
		t.Error("a response larger than the cap was accepted")
	}
}

// Deploying the same entry twice is now the expected thing to do, and two raw
// servers on one host port is the way it fails: the second stack comes up,
// cannot bind, and stops — with the reason in a log nobody is watching. The
// form is where that gets said.
func TestSecondServerOnTheSamePortIsRefused(t *testing.T) {
	s, database := catalogTestServer(t)
	keyring, err := secrets.LoadOrCreateKey(filepath.Join(t.TempDir(), "master.key"))
	if err != nil {
		t.Fatal(err)
	}
	s.keyring = keyring

	const stack = "services:\n  mc:\n    image: itzg/minecraft-server\n    ports:\n      - \"${HOST_PORT}:25565\"\n"
	first := &db.App{
		ID: "aaaa0001", Name: "Vanilla", Subdomain: "mc-vanilla", DeployType: "compose",
		ComposeYAML: stack, EnvContent: "HOST_PORT=25565", Port: 25565,
	}
	if err := db.InsertApp(database, keyring, first); err != nil {
		t.Fatal(err)
	}

	clash := &db.App{
		Name: "Modded", Subdomain: "mc-modded", DeployType: "compose",
		ComposeYAML: stack, EnvContent: "HOST_PORT=25565", Port: 25565,
	}
	if msg := s.validateNewApp(clash); msg == "" {
		t.Error("a second server on the same host port was accepted")
	} else if !strings.Contains(msg, "25565") || !strings.Contains(msg, "Vanilla") {
		t.Errorf("the message names neither the port nor who holds it: %q", msg)
	}

	// The same entry on a port of its own is the whole point, and has to pass.
	ok := &db.App{
		Name: "Modded", Subdomain: "mc-modded", DeployType: "compose",
		ComposeYAML: stack, EnvContent: "HOST_PORT=25566", Port: 25565,
	}
	if msg := s.validateNewApp(ok); msg != "" {
		t.Errorf("a second server on its own port was refused: %q", msg)
	}
}

func TestNameFromURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://example.com/games.yaml":     "games",
		"https://example.com/a/b/mine.yml":   "mine",
		"https://example.com":                "Imported catalogue",
		"https://example.com/catalogue.yaml": "catalogue",
	} {
		if got := nameFromURL(in); got != want {
			t.Errorf("nameFromURL(%q) = %q, want %q", in, got, want)
		}
	}
}
