package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"quasar/internal/catalog"
	"quasar/internal/db"
)

// postEntry submits the entry form for one catalogue, with the path values the
// router would have set.
func postEntry(t *testing.T, s *Server, catID, entryID string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("POST", "/settings/catalogs/"+catID+"/entries/"+entryID, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.SetPathValue("id", catID)
	r.SetPathValue("entry", entryID)
	w := httptest.NewRecorder()
	s.handleCatalogEntrySave(w, r)
	return w
}

// minecraftForm is the fleet case filled in by hand: a compose entry with the
// choices that make one entry cover every server the operator wants.
func minecraftForm() url.Values {
	return url.Values{
		"id": {"minecraft-modded"}, "name": {"Modded Minecraft"},
		"description": {"Fabric or Forge, version of your choosing"},
		"category":    {"Minecraft"}, "deploy_type": {"compose"},
		"compose":         {"services:\n  mc:\n    image: itzg/minecraft-server\n    ports:\n      - \"${HOST_PORT}:25565\"\n"},
		"compose_service": {"mc"}, "port": {"25565"}, "raw": {"1"},
		"app_name": {"Minecraft {{VERSION}} ({{TYPE}})"}, "subdomain": {"mc-{{TYPE}}-{{VERSION}}"},
		"env": {"TYPE={{TYPE}}\nVERSION={{VERSION}}\nHOST_PORT={{HOST_PORT}}"},

		"param_name":    {"TYPE", "VERSION", "HOST_PORT"},
		"param_label":   {"Mod loader", "Version", "Port"},
		"param_kind":    {"select", "text", "port"},
		"param_default": {"FABRIC", "1.20.1", "25566"},
		"param_options": {"FABRIC, FORGE, NEOFORGE", "", ""},
		"param_help":    {"", "Pick what your mods are built for.", ""},
	}
}

// A template that reaches through a nil pointer does not fail loudly: it
// stops writing where it tripped and serves what it had already produced. The
// new-entry form did exactly that — dying at the parameter rows and taking the
// Add button, the row template and the whole script with it, so the page looked
// nearly right and none of it worked. Rendering to io.Discard and checking for
// an error is not enough to see that; the end of the page has to be asserted.
func TestNewEntryFormIsWholePage(t *testing.T) {
	s, database := catalogTestServer(t)
	if _, err := db.InsertCatalog(database, &db.Catalog{Name: "Mine", YAML: "name: Mine\n", Enabled: true}); err != nil {
		t.Fatal(err)
	}

	r := httptest.NewRequest("GET", "/settings/catalogs/1/entries/new", nil)
	r.SetPathValue("id", "1")
	r.SetPathValue("entry", "new")
	w := httptest.NewRecorder()
	s.handleCatalogEntryForm(w, r)

	body := w.Body.String()
	for _, want := range []string{
		`id="add-param"`,          // the button that adds a choice
		`id="param-row-template"`, // the row it clones
		`id="image-fields"`,       // the panels the deploy picker swaps
		`id="compose-fields"`,
		"</script>", // the script that drives both
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the form stops short of %s — the render aborted before the end of the page", want)
		}
	}
}

// The form and the textarea edit the same document, so an entry written in one
// has to come back out of the other — parameters, options and all.
func TestEntryFormWritesTheDocument(t *testing.T) {
	s, database := catalogTestServer(t)
	id, err := db.InsertCatalog(database, &db.Catalog{Name: "Mine", YAML: "name: Mine\n", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}

	w := postEntry(t, s, "1", "new", minecraftForm())
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status %d, want a redirect — the entry was rejected:\n%s", w.Code, w.Body)
	}

	row := db.GetCatalog(database, id)
	c, err := catalog.Parse(row.Name, row.YAML)
	if err != nil {
		t.Fatalf("the form wrote a document that will not parse: %v\n%s", err, row.YAML)
	}
	e := c.Get("minecraft-modded")
	if e == nil {
		t.Fatalf("the entry is not in the document:\n%s", row.YAML)
	}
	if len(e.Params) != 3 {
		t.Fatalf("%d parameters survived, want 3: %+v", len(e.Params), e.Params)
	}
	if got := e.Params[0].Options; len(got) != 3 || got[0] != "FABRIC" {
		t.Errorf("the select's options did not survive: %v", got)
	}
	if e.Params[2].Type() != "port" {
		t.Errorf("parameter kind = %q, want port", e.Params[2].Type())
	}
	// The category the entry invented has to be declared, or Grouped drops the
	// entry and it never appears on the page at all.
	if len(c.Grouped()) == 0 {
		t.Errorf("the entry's category was never declared: %v", c.Categories)
	}

	// And the entry has to work: picking values fills the form in.
	f := e.Fill(e.Resolve(catalog.Values{"TYPE": "FORGE", "VERSION": "1.21"}), "x", "x.example.com")
	if !strings.Contains(f.Env, "TYPE=FORGE") || f.Name != "Minecraft 1.21 (FORGE)" {
		t.Errorf("an entry written in the form does not resolve its choices: %+v", f)
	}
}

// A row left blank is somebody who clicked Add and changed their mind, not an
// error to hand back.
func TestEntryFormIgnoresBlankParameterRows(t *testing.T) {
	s, database := catalogTestServer(t)
	if _, err := db.InsertCatalog(database, &db.Catalog{Name: "Mine", YAML: "name: Mine\n", Enabled: true}); err != nil {
		t.Fatal(err)
	}

	form := url.Values{
		"id": {"nginx"}, "name": {"Nginx"}, "description": {"A web server"},
		"category": {"Websites"}, "deploy_type": {"image"}, "image_ref": {"nginx:latest"}, "port": {"80"},
		"param_name": {"", ""}, "param_kind": {"text", "text"},
	}
	if w := postEntry(t, s, "1", "new", form); w.Code != http.StatusSeeOther {
		t.Fatalf("status %d, want a redirect:\n%s", w.Code, w.Body)
	}
	c, _ := catalog.Parse("Mine", db.GetCatalog(database, 1).YAML)
	if e := c.Get("nginx"); e == nil || len(e.Params) != 0 {
		t.Errorf("blank rows became parameters: %+v", e)
	}
}

// An entry that would not survive the catalogue's own checks is handed back to
// the form with the reasons, and the document is left as it was.
func TestEntryFormRejectsAnIncompleteEntry(t *testing.T) {
	s, database := catalogTestServer(t)
	const before = "name: Mine\n"
	if _, err := db.InsertCatalog(database, &db.Catalog{Name: "Mine", YAML: before, Enabled: true}); err != nil {
		t.Fatal(err)
	}

	form := minecraftForm()
	form.Set("compose", "") // a compose entry with no stack
	w := postEntry(t, s, "1", "new", form)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d, want the form back with the errors on it", w.Code)
	}
	if !strings.Contains(w.Body.String(), "compose file") {
		t.Error("the form does not say what is wrong")
	}
	// What was typed has to come back, or the operator retypes the lot.
	if !strings.Contains(w.Body.String(), "FABRIC, FORGE, NEOFORGE") {
		t.Error("the parameter rows were not handed back")
	}
	if got := db.GetCatalog(database, 1).YAML; got != before {
		t.Errorf("the document was written anyway:\n%s", got)
	}
}

// Editing an entry replaces it where it stood rather than adding a second one,
// including when the id itself is what changed.
func TestEntryFormEditReplacesInPlace(t *testing.T) {
	s, database := catalogTestServer(t)
	if _, err := db.InsertCatalog(database, &db.Catalog{Name: "Mine", YAML: "name: Mine\n", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	postEntry(t, s, "1", "new", minecraftForm())

	form := minecraftForm()
	form.Set("id", "minecraft-fabric")
	form.Set("name", "Fabric Minecraft")
	if w := postEntry(t, s, "1", "minecraft-modded", form); w.Code != http.StatusSeeOther {
		t.Fatalf("status %d, want a redirect:\n%s", w.Code, w.Body)
	}

	c, _ := catalog.Parse("Mine", db.GetCatalog(database, 1).YAML)
	if len(c.Templates) != 1 {
		t.Fatalf("%d entries, want 1 — the edit added one instead of replacing it", len(c.Templates))
	}
	if c.Templates[0].ID != "minecraft-fabric" {
		t.Errorf("entry id = %q, want the edited one", c.Templates[0].ID)
	}
}

func TestEntryFormDeleteRemovesOne(t *testing.T) {
	s, database := catalogTestServer(t)
	if _, err := db.InsertCatalog(database, &db.Catalog{Name: "Mine", YAML: "name: Mine\n", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	postEntry(t, s, "1", "new", minecraftForm())

	r := httptest.NewRequest("POST", "/settings/catalogs/1/entries/minecraft-modded/delete", nil)
	r.SetPathValue("id", "1")
	r.SetPathValue("entry", "minecraft-modded")
	w := httptest.NewRecorder()
	s.handleCatalogEntryDelete(w, r)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status %d, want a redirect", w.Code)
	}

	c, _ := catalog.Parse("Mine", db.GetCatalog(database, 1).YAML)
	if len(c.Templates) != 0 {
		t.Errorf("%d entries left, want none", len(c.Templates))
	}
}

// Starting empty is the way in for somebody who does not want to write YAML at
// all, so it must not be held to "declares no entries".
func TestStartCreatesAnEmptyCatalogue(t *testing.T) {
	s, database := catalogTestServer(t)

	w := post(t, s.handleCatalogStart, "/settings/catalogs/start", url.Values{"name": {"My servers"}})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status %d, want a redirect:\n%s", w.Code, w.Body)
	}
	if got := w.Header().Get("Location"); !strings.HasSuffix(got, "/entries/new") {
		t.Errorf("redirected to %q, want the form for its first entry", got)
	}
	rows, _ := db.ListCatalogs(database)
	if len(rows) != 1 || rows[0].Name != "My servers" {
		t.Fatalf("stored %+v, want one empty catalogue", rows)
	}
	if _, err := catalog.Parse(rows[0].Name, rows[0].YAML); err != nil {
		t.Errorf("an empty catalogue does not parse: %v", err)
	}
}
