package vps

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// distroID reads which distribution the host runs: the ID line of its
// os-release(5). Inside the dashboard container HOST_ETC points at the host's
// /etc (the same variable gopsutil reads), so the answer is the server's
// distribution rather than Alpine's.
//
// /usr/lib/os-release is the file the standard names as the fallback, and the
// one /etc/os-release is a symlink to on most distributions. It is reached
// through HOST_ROOT, because a relative symlink resolves inside the mount but
// an absolute one would resolve inside this container.
//
// ok is false when neither can be read — the host is not Linux, or the mounts
// are missing — which is different from a Linux nobody here has an icon for.
func distroID() (id string, ok bool) {
	paths := []string{
		filepath.Join(envOr("HOST_ETC", "/etc"), "os-release"),
		filepath.Join(envOr("HOST_ROOT", "/"), "usr", "lib", "os-release"),
	}
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		id, err := parseOSReleaseID(f)
		_ = f.Close()
		return id, err == nil
	}
	return "", false
}

func parseOSReleaseID(r io.Reader) (string, error) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		key, value, found := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		if !found || key != "ID" {
			continue
		}
		// Values may be quoted, single or double, and a double-quoted one may
		// carry shell escapes.
		if v, err := strconv.Unquote(value); err == nil {
			value = v
		} else {
			value = strings.Trim(value, `'"`)
		}
		return strings.ToLower(value), nil
	}
	return "", sc.Err()
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// distroIcons maps an os-release ID to the icon drawn for it, named after its
// Simple Icons slug. An ID missing from here is drawn as Tux rather than as
// whatever its ID_LIKE says: a distribution wearing its parent's logo is
// claiming to be something it is not, while Tux is only saying "Linux".
var distroIcons = map[string]string{
	"almalinux":  "almalinux",
	"alpine":     "alpinelinux",
	"arch":       "archlinux",
	"centos":     "centos",
	"debian":     "debian",
	"elementary": "elementary",
	"fedora":     "fedora",
	"gentoo":     "gentoo",
	"kali":       "kalilinux",
	"linuxmint":  "linuxmint",
	"manjaro":    "manjaro",
	"mx":         "mxlinux",
	"nixos":      "nixos",
	"ol":         "oracle",
	"opensuse":   "opensuse",
	"pop":        "popos",
	"raspbian":   "raspberrypi",
	"rhel":       "redhat",
	"rocky":      "rockylinux",
	"sled":       "suse",
	"sles":       "suse",
	"ubuntu":     "ubuntu",
	"void":       "voidlinux",
	"zorin":      "zorin",
}

// distroIcon names the icon for a distribution. Every openSUSE flavour
// ("opensuse-leap", "opensuse-tumbleweed", "opensuse-microos"…) has an ID of
// its own and one logo between them.
func distroIcon(id string) string {
	if strings.HasPrefix(id, "opensuse") {
		id = "opensuse"
	}
	if icon, ok := distroIcons[id]; ok {
		return icon
	}
	return "linux"
}

// DistroIconNames lists every icon distroIcon can name, so that a test can
// check each of them is actually drawn: one added here but not to the
// template would otherwise quietly render as the generic server.
func DistroIconNames() []string {
	names := []string{"linux"}
	for _, icon := range distroIcons {
		if !slices.Contains(names, icon) {
			names = append(names, icon)
		}
	}
	slices.Sort(names)
	return names
}
