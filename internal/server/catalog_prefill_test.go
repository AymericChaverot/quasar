package server

import (
	"bytes"
	"html"
	"strings"
	"testing"

	"quasar/internal/catalog"
	"quasar/internal/db"
)

// renderAppNew renders the new-application page the way handleAppNew does for
// a chosen catalogue entry, with the entry's parameters left at their defaults.
func renderAppNew(t *testing.T, id string) string {
	t.Helper()
	return renderAppNewWith(t, id, nil)
}

// renderAppNewWith is the same for an entry whose parameters were answered.
// It mirrors handleAppNew rather than calling it: the handler reaches for the
// database to find a free subdomain, and none of what is asserted here needs
// one.
func renderAppNewWith(t *testing.T, id string, picked catalog.Values) string {
	t.Helper()
	s := testServer(t)
	cat := catalog.Builtin()
	e := cat.Get(id)
	if e == nil {
		t.Fatalf("catalogue has no entry %q", id)
	}
	v := e.Resolve(picked)
	sub := e.SubdomainFor(v)
	f := e.Fill(v, sub, sub+".example.com")
	data := map[string]any{
		"Title": "New", "Domain": "example.com",
		"Catalog": cat.Grouped(),
		"Picked":  e,
		"Values":  v,
		"Form": &db.App{
			Name: f.Name, Subdomain: f.Subdomain, DeployType: f.DeployType,
			ImageRef: f.ImageRef, ComposeYAML: f.Compose, ComposeService: f.ComposeService,
			Port: f.Port, DataMount: f.DataMount, EnvContent: f.Env,
		},
	}
	var buf bytes.Buffer
	if err := s.pages["app_new"].ExecuteTemplate(&buf, "layout", data); err != nil {
		t.Fatalf("render app_new: %v", err)
	}
	return buf.String()
}

// A compose entry has to arrive at the form as a compose deploy: the type
// selected, the stack in the textarea, and the routed service carried in the
// hidden field. Prefilling it as an image would silently create an app with an
// empty image reference.
func TestComposeTemplatePrefillsTheComposeForm(t *testing.T) {
	html := renderAppNew(t, "immich")

	if !strings.Contains(html, `value="compose" checked`) {
		t.Error("compose deploy type is not the selected radio")
	}
	if !strings.Contains(html, "immich-machine-learning") {
		t.Error("compose file was not prefilled into the textarea")
	}
	if !strings.Contains(html, `name="compose_service" value="immich-server"`) {
		t.Error("routed service was not carried into the form")
	}
	// The generated secret has to reach the env box, not just the struct.
	if strings.Contains(html, "{{RANDOM}}") {
		t.Error("an unrendered secret placeholder reached the page")
	}
}

// The address an entry needs in its env is one Quasar already knows, so it
// should arrive filled in rather than as something to paste back by hand.
func TestPublicAddressIsFilledInFromTheSubdomain(t *testing.T) {
	html := renderAppNew(t, "ghost")
	if !strings.Contains(html, "https://ghost.example.com") {
		t.Error("the entry's public URL was not resolved from the subdomain and domain")
	}
	if strings.Contains(html, "CHANGE-ME") {
		t.Error("a placeholder address reached the form")
	}
}

func TestImageTemplateStillPrefillsTheImageForm(t *testing.T) {
	html := renderAppNew(t, "jellyfin")

	if !strings.Contains(html, `value="image" checked`) {
		t.Error("image deploy type is not the selected radio")
	}
	if !strings.Contains(html, "jellyfin/jellyfin:latest") {
		t.Error("image reference was not prefilled")
	}
}

// Entries carrying a caveat have to show it; the raw-port note is the only
// warning a game server or a database gets that its subdomain is inert.
func TestNoteIsShownForEntriesThatCarryOne(t *testing.T) {
	html := renderAppNew(t, "minecraft")
	if !strings.Contains(html, "alert-note") {
		t.Error("entry with a Note rendered no note")
	}
	if !strings.Contains(html, "25565") {
		t.Error("the raw port the server is actually reached on is not on the page")
	}
}

// An entry that asks something has to ask it on the page: a panel with a field
// per parameter, defaults filled in, submitting back to the same handler. If
// the panel never renders, the entry is unreachable — its card does not link
// anywhere.
func TestParameterisedEntryOffersItsChoices(t *testing.T) {
	page := renderAppNew(t, "minecraft")

	if !strings.Contains(page, `id="catalog-params-minecraft"`) {
		t.Fatal("no parameter panel for an entry that declares parameters")
	}
	if !strings.Contains(page, `name="p.VERSION"`) || !strings.Contains(page, `name="p.TYPE"`) {
		t.Error("the parameters did not reach the panel as fields")
	}
	// A select has to offer its options, and start on the declared default.
	if !strings.Contains(page, `<option value="PAPER"`) {
		t.Error("a select parameter rendered without its options")
	}
	if !strings.Contains(page, `<option value="VANILLA" selected`) {
		t.Error("the select did not start on the entry's default")
	}
	// The card must not link straight to the form, or the choices are skipped.
	if strings.Contains(page, `href="/apps/new?template=minecraft"`) {
		t.Error("the card links past the choices it is meant to ask")
	}
}

// Picking values has to change what the form is prefilled with — the whole
// point of the feature — including the address, so a second server from the
// same entry does not land on the first one's subdomain.
func TestPickedParametersReachTheForm(t *testing.T) {
	page := renderAppNewWith(t, "minecraft", catalog.Values{"TYPE": "PAPER", "VERSION": "1.20.1"})

	for _, want := range []string{"TYPE=PAPER", "MINECRAFT_VERSION=1.20.1"} {
		if !strings.Contains(page, want) {
			t.Errorf("%q is not in the prefilled env", want)
		}
	}
	if !strings.Contains(page, `value="mc-paper-1-20-1"`) {
		t.Error("the subdomain does not carry what was picked")
	}
	if !strings.Contains(page, "Minecraft 1.20.1 (PAPER)") {
		t.Error("the proposed application name does not carry what was picked")
	}
}

// The category row is the second way through the catalogue, and it filters on
// data-cat — which the cards have to carry for it to match anything.
func TestCategoryFilterHasSomethingToFilterOn(t *testing.T) {
	page := renderAppNew(t, "jellyfin")
	if !strings.Contains(page, `class="chip chip-filter is-on" data-cat=""`) {
		t.Error("no All chip, so a picked category could not be cleared")
	}
	for _, g := range catalog.Builtin().Grouped() {
		want := `data-cat="` + html.EscapeString(g.Category) + `"`
		if strings.Count(page, want) < 2 {
			t.Errorf("category %q has a chip or its cards, not both", g.Category)
		}
	}
}

// Every category heading should reach the page, or the catalogue silently
// shrinks to whatever the template happens to loop over.
func TestEveryCategoryReachesThePage(t *testing.T) {
	page := renderAppNew(t, "jellyfin")
	for _, g := range catalog.Builtin().Grouped() {
		// Half the category names contain an ampersand, which the template
		// escaper writes as &amp;.
		if !strings.Contains(page, html.EscapeString(g.Category)) {
			t.Errorf("category %q is missing from the page", g.Category)
		}
	}
}
