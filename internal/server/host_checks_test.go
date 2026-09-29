package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"quasar/internal/db"
	"quasar/internal/hostupdate"
)

// The same updates are the same news, whatever order they were listed in; a
// new version of one of them is not.
func TestFingerprint(t *testing.T) {
	a := []hostupdate.Package{{Name: "curl", Version: "8.9.1"}, {Name: "kernel", Version: "6.11.4"}}
	b := []hostupdate.Package{{Name: "kernel", Version: "6.11.4"}, {Name: "curl", Version: "8.9.1"}}
	c := []hostupdate.Package{{Name: "kernel", Version: "6.11.5"}, {Name: "curl", Version: "8.9.1"}}
	if fingerprint(a) != fingerprint(b) {
		t.Error("the order of the list changed its fingerprint")
	}
	if fingerprint(a) == fingerprint(c) {
		t.Error("a new version did not change the fingerprint")
	}
}

func TestHostUpdatesMessage(t *testing.T) {
	one := hostUpdatesMessage([]hostupdate.Package{{Name: "curl"}}, false)
	if !strings.Contains(one, "1 update waiting") || strings.Contains(one, "Docker") || strings.Contains(one, "restart is also") {
		t.Errorf("one update: %q", one)
	}
	many := hostUpdatesMessage([]hostupdate.Package{{Name: "curl"}, {Name: "docker-ce"}}, true)
	for _, want := range []string{"2 updates waiting", "Docker among them", "restart is also needed"} {
		if !strings.Contains(many, want) {
			t.Errorf("%q does not say %q", many, want)
		}
	}
}

// A list is announced once: the second time round it is remembered, and a
// changed list is announced again.
func TestAnnounceHostUpdatesOnce(t *testing.T) {
	s := fakeHost(t, managedHost...)
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	s.db = database
	m := hostupdate.Detect(s.cfg.HostRootPath, "fedora")
	store := s.hostStore()
	if err := os.MkdirAll(store.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(list string) {
		if err := os.WriteFile(filepath.Join(store.Dir, "updates.txt"), []byte(list), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	write("kernel.x86_64  6.11.4-301.fc41  updates\n")
	s.announceHostUpdates(m)
	first := db.GetSetting(database, settingHostNotified)
	if first == "" {
		t.Fatal("the updates were not remembered as announced")
	}
	write("kernel.x86_64  6.11.5-301.fc41  updates\n")
	s.announceHostUpdates(m)
	if db.GetSetting(database, settingHostNotified) == first {
		t.Fatal("a new list was not announced")
	}
}
