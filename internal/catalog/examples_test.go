package catalog

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The catalogues in catalogs/ are documentation meant to be pasted into a
// running Quasar, or fetched from the repository by one. Nothing compiles them,
// so nothing would notice them going stale — a key renamed here parses in Go
// and fails on somebody else's server, at the moment they were trying the
// feature for the first time. They are held to what the page holds a pasted
// document to, and to the one thing they exist to show.
func TestExampleCatalogsAreAccepted(t *testing.T) {
	dir := filepath.Join("..", "..", "catalogs")
	paths, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		t.Fatalf("looking for example catalogues: %v", err)
	}
	if len(paths) == 0 {
		t.Fatalf("no example catalogue in %s; the folder is referenced from the README", dir)
	}

	for _, path := range paths {
		name := strings.TrimSuffix(filepath.Base(path), ".yaml")
		t.Run(name, func(t *testing.T) {
			doc, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("reading: %v", err)
			}
			c, err := Parse(name, string(doc))
			if err != nil {
				t.Fatalf("would not be accepted on paste: %v", err)
			}
			if len(c.Templates) == 0 {
				t.Fatal("declares no entries")
			}
			for _, err := range c.Validate() {
				t.Error(err)
			}

			// Parameters are why the folder is there: an example of the plain
			// shape is the one already on the page a catalogue is written on.
			if !slices.ContainsFunc(c.Templates, func(e Template) bool { return len(e.Params) > 0 }) {
				t.Error("no entry here asks anything; these examples exist to show parameters")
			}

			// Merging is the only thing that happens to a catalogue after it is
			// saved, and the way to lose an entry to it is to file it under a
			// category the document never declares.
			merged := Builtin().Merge(c)
			var shown []string
			for _, g := range merged.Grouped() {
				for _, e := range g.Templates {
					shown = append(shown, e.ID)
				}
			}
			for _, e := range c.Templates {
				if !slices.Contains(shown, e.ID) {
					t.Errorf("entry %q does not appear on the page once merged, under category %q", e.ID, e.Category)
				}
			}
		})
	}
}
