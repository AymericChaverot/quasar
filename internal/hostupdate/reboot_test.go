package hostupdate

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRebootRequired(t *testing.T) {
	booted := time.Now().Add(-24 * time.Hour)
	before, after := booted.Add(-time.Hour), booted.Add(time.Hour)

	cases := []struct {
		name    string
		modules map[string]time.Time // kernel directories and when they appeared
		flag    bool                 // /run/reboot-required
		want    bool
	}{
		{"nothing new", map[string]time.Time{"6.11.3-300.fc41": before}, false, false},
		{"Debian's flag", map[string]time.Time{"6.11.3-300.fc41": before}, true, true},
		{"a newer kernel beside the running one", map[string]time.Time{
			"6.11.3-300.fc41": before, "6.11.4-301.fc41": after}, false, true},
		{"the running kernel replaced in place", map[string]time.Time{
			"6.11.4-301.fc41": after}, false, true},
		// A module rebuilt for an older kernel still installed is not a
		// reason to restart.
		{"an older kernel touched", map[string]time.Time{
			"6.11.3-300.fc41": before, "6.10.9-200.fc41": after}, false, false},
		// A container VPS: the kernel is the provider's, never installed here.
		{"a kernel that was never here", map[string]time.Time{
			"5.15.0-100-generic": before}, false, false},
	}
	for _, c := range cases {
		root := t.TempDir()
		for name, when := range c.modules {
			dir := filepath.Join(root, "usr", "lib", "modules", name)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(dir, when, when); err != nil {
				t.Fatal(err)
			}
		}
		if c.flag {
			touch(t, root, "run/reboot-required")
		}
		if got := RebootRequired(root, "6.11.3-300.fc41", booted); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestNewerKernel(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"6.11.4-301.fc41", "6.11.3-300.fc41", true},
		{"6.10.1", "6.9.12", true},
		{"6.9.12", "6.10.1", false},
		{"6.11.3", "6.11.3", false},
		{"6.11.3.1", "6.11.3", true},
		{"extramodules", "6.11.3", false},
	}
	for _, c := range cases {
		if got := newerKernel(c.a, c.b); got != c.want {
			t.Errorf("newerKernel(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}
