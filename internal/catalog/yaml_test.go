package catalog

import (
	"reflect"
	"strings"
	"testing"
)

const sampleDoc = `
name: My servers
categories: [Minecraft]
entries:
  - id: mc
    name: Minecraft
    description: A server of my own
    category: Minecraft
    deploy_type: compose
    compose_service: mc
    port: 25565
    raw: true
    app_name: "Minecraft {{VERSION}}"
    subdomain: "mc-{{VERSION}}"
    params:
      - {name: VERSION, label: Version, default: "1.20.1"}
    env: |
      VERSION={{VERSION}}
    compose: |
      services:
        mc:
          image: itzg/minecraft-server:latest
`

func parseOK(t *testing.T, doc string) Catalog {
	t.Helper()
	c, err := Parse("fallback", doc)
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	if errs := c.Validate(); len(errs) > 0 {
		t.Fatalf("valid document rejected: %v", errs)
	}
	return c
}

func TestParseReadsACatalogue(t *testing.T) {
	c := parseOK(t, sampleDoc)

	if c.Name != "My servers" {
		t.Errorf("name = %q, want the one on the document", c.Name)
	}
	e := c.Get("mc")
	if e == nil {
		t.Fatal("the entry did not survive parsing")
	}
	if e.Source != "My servers" {
		t.Errorf("source = %q, want the catalogue's name", e.Source)
	}
	if len(e.Params) != 1 || e.Params[0].Name != "VERSION" {
		t.Fatalf("parameters did not survive parsing: %v", e.Params)
	}
	if !strings.Contains(e.Compose, "itzg/minecraft-server") {
		t.Error("the compose file did not survive parsing")
	}
}

// A key that is nearly right is the likeliest mistake in a hand-written entry,
// and the one YAML would otherwise accept in silence: "deploytype" would parse,
// leave DeployType empty, and deploy a compose stack as a single image.
func TestParseRejectsAKeyItDoesNotKnow(t *testing.T) {
	_, err := Parse("x", "entries:\n  - id: mc\n    deploytype: compose\n")
	if err == nil {
		t.Fatal("a misspelled key was accepted")
	}
	if !strings.Contains(err.Error(), "deploytype") {
		t.Errorf("the error does not name the offending key: %v", err)
	}
}

func TestYAMLRoundTrips(t *testing.T) {
	c := parseOK(t, sampleDoc)
	out, err := c.YAML()
	if err != nil {
		t.Fatalf("writing it back out: %v", err)
	}
	again := parseOK(t, out)

	if len(again.Templates) != len(c.Templates) {
		t.Fatalf("%d entries survived a round trip, started with %d", len(again.Templates), len(c.Templates))
	}
	if !reflect.DeepEqual(again.Get("mc"), c.Get("mc")) {
		t.Errorf("the entry changed on a round trip:\n%+v\n%+v", again.Get("mc"), c.Get("mc"))
	}
}

func TestValidateReportsWhatIsWrong(t *testing.T) {
	cases := map[string]string{
		"an entry with no id":               "entries:\n  - name: X\n",
		"an id that is not a DNS label":     "entries:\n  - {id: My_App, name: X, description: d, category: c, port: 80, image_ref: nginx}\n",
		"two entries sharing an id":         "entries:\n  - {id: a, name: X, description: d, category: c, port: 80, image_ref: nginx}\n  - {id: a, name: Y, description: d, category: c, port: 80, image_ref: nginx}\n",
		"a port out of range":               "entries:\n  - {id: a, name: X, description: d, category: c, port: 0, image_ref: nginx}\n",
		"an image entry with no image":      "entries:\n  - {id: a, name: X, description: d, category: c, port: 80}\n",
		"a compose entry with no stack":     "entries:\n  - {id: a, name: X, description: d, category: c, port: 80, deploy_type: compose}\n",
		"a compose file that is not YAML":   "entries:\n  - id: a\n    name: X\n    description: d\n    category: c\n    port: 80\n    deploy_type: compose\n    compose: \"services: [oh, dear\"\n",
		"a service the stack never defines": "entries:\n  - id: a\n    name: X\n    description: d\n    category: c\n    port: 80\n    deploy_type: compose\n    compose_service: web\n    compose: |\n      services:\n        api:\n          image: nginx\n",
		"a select with no options":          "entries:\n  - {id: a, name: X, description: d, category: c, port: 80, image_ref: nginx, params: [{name: V, kind: select, default: x}]}\n",
		"a default the parameter refuses":   "entries:\n  - {id: a, name: X, description: d, category: c, port: 80, image_ref: nginx, params: [{name: V, kind: port, default: nope}]}\n",
		"a placeholder nothing declares":    "entries:\n  - {id: a, name: X, description: d, category: c, port: 80, image_ref: \"nginx:{{VERISON}}\"}\n",
		"a placeholder in the card name":    "entries:\n  - {id: a, name: \"X {{V}}\", description: d, category: c, port: 80, image_ref: nginx, params: [{name: V, default: '1'}]}\n",
	}
	for name, doc := range cases {
		c, err := Parse("x", doc)
		if err != nil {
			t.Errorf("%s: did not even parse: %v", name, err)
			continue
		}
		if errs := c.Validate(); len(errs) == 0 {
			t.Errorf("%s was accepted", name)
		}
	}
}

// The checks an operator's catalogue is held to are the ones the built-in
// entries have always been held to by the tests here. If the entries Quasar
// ships cannot pass them, they are the wrong checks.
func TestBuiltinCatalogValidates(t *testing.T) {
	for _, err := range Builtin().Validate() {
		t.Error(err)
	}
}

// The example is the format's documentation, shown on the page where a
// catalogue is written. Documentation that would be rejected on paste is worse
// than none.
func TestExampleIsACatalogueQuasarAccepts(t *testing.T) {
	c := parseOK(t, Example)
	if len(c.Templates) == 0 {
		t.Fatal("the example declares no entries")
	}
	e := c.Templates[0]
	if len(e.Params) == 0 {
		t.Error("the example does not show the parameters it exists to show")
	}
	// It has to survive the merge too: an example that collided with a
	// built-in id would demonstrate an override rather than an addition.
	if Builtin().Get(e.ID) != nil {
		t.Errorf("the example's entry id %q is a built-in one", e.ID)
	}
}

func TestMergeOverridesByID(t *testing.T) {
	base := Catalog{
		Categories: []string{"Games"},
		Templates: []Template{
			{ID: "mc", Name: "Built-in Minecraft", Category: "Games"},
			{ID: "valheim", Name: "Valheim", Category: "Games"},
		},
	}
	mine, err := Parse("Mine", `
entries:
  - {id: mc, name: My Minecraft, description: d, category: Modded, port: 25565, image_ref: itzg/minecraft-server}
  - {id: terraria, name: Terraria, description: d, category: Games, port: 7777, image_ref: ryshe/terraria}
`)
	if err != nil {
		t.Fatal(err)
	}
	merged := base.Merge(mine)

	if got := merged.Get("mc").Name; got != "My Minecraft" {
		t.Errorf("the operator's entry did not replace the built-in one: %q", got)
	}
	if got := merged.Get("mc").Source; got != "Mine" {
		t.Errorf("source = %q, want the catalogue that supplied it", got)
	}
	if n := len(merged.Templates); n != 3 {
		t.Errorf("%d entries after the merge, want 3 — an override is not an addition", n)
	}
	// An overriding entry keeps the position the built-in one held, so the
	// page does not reshuffle when somebody customises one card.
	if merged.Templates[0].ID != "mc" {
		t.Errorf("the override moved: entries are %v", ids(merged.Templates))
	}
	// The category only the operator's entry mentions has to be picked up, or
	// Grouped drops the entry and it silently never appears.
	if merged.Get("terraria") == nil {
		t.Fatal("the added entry went missing")
	}
	var seen bool
	for _, g := range merged.Grouped() {
		if g.Category == "Modded" {
			seen = true
		}
	}
	if !seen {
		t.Errorf("a category only an entry declared never reached Grouped: %v", merged.Categories)
	}
}

func ids(ts []Template) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.ID
	}
	return out
}
