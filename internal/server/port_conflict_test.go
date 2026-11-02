package server

import (
	"path/filepath"
	"strings"
	"testing"

	"quasar/internal/db"
	"quasar/internal/secrets"
)

// A stack is told off for a host port another application already publishes —
// but only for one the daemon would really refuse: the same number over the
// other protocol, or on another address, binds fine beside it.
func TestPortConflictOnlyForABindingTheDaemonWouldRefuse(t *testing.T) {
	s, database := catalogTestServer(t)
	k, err := secrets.LoadOrCreateKey(filepath.Join(t.TempDir(), "master.key"))
	if err != nil {
		t.Fatal(err)
	}
	s.keyring = k
	held := &db.App{ID: "mc1", Name: "Bedrock", Subdomain: "mc", DeployType: "compose",
		ComposeYAML: "services:\n  mc:\n    ports:\n      - \"19132:19132/udp\"\n      - \"127.0.0.1:5432:5432\"\n"}
	if err := db.InsertApp(database, k, held); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		port string
		want bool
	}{
		{"19132:19132/udp", true},
		{"19132:19132/tcp", false},
		{"19133:19132/udp", false},
		{"10.0.0.2:5432:5432", false},
		{"5432:5432", true},
	}
	for _, tc := range cases {
		a := &db.App{ID: "new", Name: "New", DeployType: "compose",
			ComposeYAML: "services:\n  x:\n    ports:\n      - \"" + tc.port + "\"\n"}
		msg := s.portConflict(a)
		if got := msg != ""; got != tc.want {
			t.Errorf("%s: conflict = %q, want one: %v", tc.port, msg, tc.want)
		}
		if tc.want && !strings.Contains(msg, `"Bedrock"`) {
			t.Errorf("%s: %q does not name the application holding the port", tc.port, msg)
		}
	}
}
