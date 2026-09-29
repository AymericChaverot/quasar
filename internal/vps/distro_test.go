package vps

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseOSReleaseID(t *testing.T) {
	cases := []struct{ file, want string }{
		{"NAME=\"Fedora Linux\"\nID=fedora\nVERSION_ID=42\n", "fedora"},
		{"ID=\"rhel\"\nID_LIKE=\"fedora\"\n", "rhel"},
		{"ID='opensuse-tumbleweed'\n", "opensuse-tumbleweed"},
		{"# comment\n  ID=Ubuntu  \n", "ubuntu"},
		// ID_LIKE must not be mistaken for ID, whichever comes first.
		{"ID_LIKE=debian\nID=linuxmint\n", "linuxmint"},
		{"NAME=Something\n", ""},
	}
	for _, c := range cases {
		if got, err := parseOSReleaseID(strings.NewReader(c.file)); err != nil || got != c.want {
			t.Errorf("parseOSReleaseID(%q) = %q, %v; want %q", c.file, got, err, c.want)
		}
	}
}

func TestDistroIcon(t *testing.T) {
	cases := map[string]string{
		"fedora":              "fedora",
		"rhel":                "redhat",
		"rocky":               "rockylinux",
		"ol":                  "oracle",
		"opensuse-leap":       "opensuse",
		"opensuse-tumbleweed": "opensuse",
		"sles":                "suse",
		// Unknown, and a derivative whose parent has a logo: both Tux.
		"amzn":   "linux",
		"":       "linux",
		"nobara": "linux",
	}
	for id, want := range cases {
		if got := distroIcon(id); got != want {
			t.Errorf("distroIcon(%q) = %q, want %q", id, got, want)
		}
	}
}

func TestDistroIDFallsBackToUsrLib(t *testing.T) {
	root := t.TempDir()
	lib := filepath.Join(root, "usr", "lib")
	if err := os.MkdirAll(lib, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lib, "os-release"), []byte("ID=debian\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOST_ETC", filepath.Join(root, "etc"))
	t.Setenv("HOST_ROOT", root)
	if id, ok := DistroID(); !ok || id != "debian" {
		t.Fatalf("DistroID() = %q, %v; want debian, true", id, ok)
	}
}

func TestDistroIDWithoutOSRelease(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOST_ETC", filepath.Join(root, "etc"))
	t.Setenv("HOST_ROOT", root)
	if id, ok := DistroID(); ok {
		t.Fatalf("DistroID() = %q, true; want not ok on a host without os-release", id)
	}
}
