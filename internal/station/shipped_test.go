package station

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The stations in stations/ are documentation meant to be pasted into a
// running Quasar, or fetched from the repository by one. Nothing compiles
// them, so nothing would notice them going stale — a key renamed here parses
// in Go and fails on somebody else's server, at the moment they were trying
// the feature for the first time.
//
// They are held to exactly what a pasted document is held to, which is the
// same standard the catalogues in catalogs/ are already kept to.
func TestShippedStationsAreAccepted(t *testing.T) {
	dir := filepath.Join("..", "..", "stations")
	paths, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		t.Fatalf("looking for shipped stations: %v", err)
	}
	if len(paths) == 0 {
		t.Fatalf("no station in %s; the folder is referenced from the README", dir)
	}

	ids := map[string]string{}
	for _, path := range paths {
		name := strings.TrimSuffix(filepath.Base(path), ".yaml")
		t.Run(name, func(t *testing.T) {
			doc, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("reading: %v", err)
			}
			s, err := Parse(string(doc))
			if err != nil {
				t.Fatalf("would not be accepted on paste: %v", err)
			}
			for _, err := range s.Validate() {
				t.Error(err)
			}

			// Two shipped stations under one id could not both be installed,
			// which would make one of them undeliverable rather than wrong.
			if held, taken := ids[s.ID]; taken {
				t.Errorf("id %q is already held by %s", s.ID, held)
			}
			ids[s.ID] = name

			// Every one of these is here to be read as an example, so it has
			// to say what it is and what it would be allowed to do.
			if s.Description == "" || s.Author == "" {
				t.Error("a shipped station should name itself and its author")
			}
			if len(s.Exports()) == 0 && len(s.UI.Tabs) > 0 {
				t.Error("this station draws tabs and exports nothing to fill them")
			}
		})
	}
}
