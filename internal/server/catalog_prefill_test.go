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
		"Catalog":    cat.Grouped(),
		"Categories": cat.Categories,
		"Picked":     e,
		"Values":     v,
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
