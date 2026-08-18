package ui

import (
	"strings"
	"testing"
)

// An iframe names one of the application's own services, and the address it
// resolves to never appears in the document or reaches the browser.
func TestParseServiceSrc(t *testing.T) {
	for _, c := range []struct {
		src     string
		ok      bool
		service string
		port    int
		path    string
	}{
		{"{{service:minecraft:8123}}", true, "minecraft", 8123, "/"},
		{"{{service:minecraft:8123}}/map/", true, "minecraft", 8123, "/map/"},
		{"{{service:web-1:80}}index.html", true, "web-1", 80, "/index.html"},

		{"https://example.com/map", false, "", 0, ""},
		{"{{service:minecraft}}", false, "", 0, ""},
		{"{{service:minecraft:0}}", false, "", 0, ""},
		{"{{service:minecraft:99999}}", false, "", 0, ""},
		{"prefix{{service:minecraft:8123}}", false, "", 0, ""},
	} {
		ref, ok := ParseServiceSrc(c.src)
		if ok != c.ok {
			t.Errorf("ParseServiceSrc(%q) ok = %v, want %v", c.src, ok, c.ok)
			continue
		}
		if !ok {
			continue
		}
		if ref.Service != c.service || ref.Port != c.port || ref.Path != c.path {
			t.Errorf("ParseServiceSrc(%q) = %+v", c.src, ref)
		}
	}
}

// An iframe pointing at a service is held to the same permission a script's own
// request to it would need. Anything else is refused at import rather than
// discovered as a blank frame.
func TestAnIframeIsValidated(t *testing.T) {
	for _, c := range []struct {
		src   string
		wants string
	}{
		{"", "needs a src"},
		{"{{service:minecraft}}", "service reference"},
		{"http://example.com/map", "https address"},
	} {
		errs := UI{Tabs: []Tab{{ID: "t", Name: "T", Panels: []Panel{
			{ID: "map", Type: "iframe", Src: c.src},
		}}}}.Validate()
		if len(errs) == 0 {
			t.Errorf("src %q was accepted", c.src)
			continue
		}
		found := false
		for _, err := range errs {
			if strings.Contains(err.Error(), c.wants) {
				found = true
			}
		}
		if !found {
			t.Errorf("src %q: no complaint mentioned %q: %v", c.src, c.wants, errs)
		}
	}

	// And the shape that works is accepted whole.
	if errs := (UI{Tabs: []Tab{{ID: "t", Name: "T", Panels: []Panel{
		{ID: "map", Type: "iframe", Src: "{{service:minecraft:8123}}/map/"},
	}}}}).Validate(); len(errs) > 0 {
		t.Errorf("a well-formed iframe was refused: %v", errs)
	}
}

// Embeds is what a station's validation holds against net.internal.
func TestEmbedsListsWhatIsPointedAt(t *testing.T) {
	u := UI{Tabs: []Tab{{ID: "t", Name: "T", Panels: []Panel{
		{ID: "map", Type: "iframe", Src: "{{service:minecraft:8123}}/"},
		{ID: "docs", Type: "iframe", Src: "https://example.com/docs"},
	}}}}

	got := u.Embeds()
	if len(got) != 1 || got[0].Service != "minecraft" || got[0].Port != 8123 {
		t.Errorf("Embeds = %+v, want only the one on this server", got)
	}
}
