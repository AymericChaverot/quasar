package station

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The specification carries a complete station, short enough to read in one
// go, and it is the first thing anybody writing one will copy. Nothing
// compiles it, so nothing would notice it going stale — a key renamed here
// parses in Go and fails on somebody else's server, at the moment they were
// trying the feature for the first time.
//
// It is held to exactly what a pasted document is held to. Documentation that
// would be refused on paste is worth less than none, which is the standard the
// catalogue's own examples are already kept to.
func TestTheDocumentedStationIsAccepted(t *testing.T) {
	doc := workedExample(t)

	s, err := Parse(doc)
	if err != nil {
		t.Fatalf("the documented station would not be accepted on paste: %v", err)
	}
	for _, err := range s.Validate() {
		t.Error(err)
	}

	// The example exists to show the three blocks a catalogue entry does not
	// have. An example that quietly lost one of them still parses.
	if len(s.Permissions.Exec.Services) == 0 || len(s.Permissions.NetExternal.Allow) == 0 {
		t.Error("the example no longer shows what permissions look like")
	}
	if len(s.UI.Tabs) < 2 {
		t.Error("the example no longer shows a tab strip")
	}
	if s.Hooks.Empty() {
		t.Error("the example no longer shows a hook")
	}
	if len(s.Exports()) < len(s.Actions()) {
		t.Errorf("the script exports %v for %v", s.Exports(), s.Actions())
	}
}

// workedExample pulls the yaml block under "## Worked example" out of the
// specification.
func workedExample(t *testing.T) string {
	t.Helper()
	path := filepath.Join("..", "..", "docs", "stations.md")
	spec, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}

	_, after, found := strings.Cut(string(spec), "\n## Worked example\n")
	if !found {
		t.Fatalf("%s no longer has a worked example; this test is the only thing keeping it true", path)
	}
	_, after, found = strings.Cut(after, "```yaml\n")
	if !found {
		t.Fatalf("%s: the worked example is no longer a yaml block", path)
	}
	doc, _, found := strings.Cut(after, "\n```")
	if !found {
		t.Fatalf("%s: the worked example's block is never closed", path)
	}
	return doc
}
