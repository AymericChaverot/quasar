package hostupdate

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestIsDocker(t *testing.T) {
	for name, want := range map[string]bool{
		"docker":                true,
		"docker-ce":             true,
		"docker-ce-cli":         true,
		"docker.io":             true,
		"docker-compose-plugin": true,
		"containerd.io":         true,
		"containerd":            true,
		"moby-engine":           true,
		"runc":                  true,
		"dockerfile-mode":       false,
		"runcible":              false,
		"kernel":                false,
		"":                      false,
	} {
		if got := IsDocker(name); got != want {
			t.Errorf("IsDocker(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestParsers(t *testing.T) {
	cases := []struct {
		name  string
		parse func(string) []Package
		raw   string
		want  []Package
	}{
		{"apt", parseApt, `Listing...
curl/noble-updates 8.5.0-2ubuntu10.6 amd64 [upgradable from: 8.5.0-2ubuntu10.5]
docker-ce/noble 5:28.5.1-1~ubuntu.24.04~noble amd64 [upgradable from: 5:28.5.0-1~ubuntu.24.04~noble]
`, []Package{{"curl", "8.5.0-2ubuntu10.6"}, {"docker-ce", "5:28.5.1-1~ubuntu.24.04~noble"}}},

		{"dnf", parseRPM, `
kernel.x86_64                     6.11.4-301.fc41          updates
docker-ce.x86_64                  3:28.5.1-1.fc41          docker-ce-stable
Obsoleting Packages
grub2-tools.x86_64                1:2.12-10.fc41           updates
`, []Package{{"kernel", "6.11.4-301.fc41"}, {"docker-ce", "3:28.5.1-1.fc41"}}},

		{"zypper", parseZypper, `Loading repository data...
Reading installed packages...
S | Repository | Name | Current Version | Available Version | Arch
--+------------+------+-----------------+-------------------+-------
v | Main       | curl | 8.6.0-1.1       | 8.7.1-1.1         | x86_64
v | Main       | docker | 27.5.1-1.1    | 28.5.1-1.1        | x86_64
`, []Package{{"curl", "8.7.1-1.1"}, {"docker", "28.5.1-1.1"}}},

		{"pacman", parsePacman, `linux 6.11.3.arch1-1 -> 6.11.4.arch1-1
docker 1:28.5.0-1 -> 1:28.5.1-1 [ignored]
`, []Package{{"linux", "6.11.4.arch1-1"}, {"docker", "1:28.5.1-1"}}},

		{"apk", parseApk, `Installed:                                Available:
busybox-1.36.1-r29                      < 1.36.1-r30
py3-setuptools-70.3.0-r0                < 75.1.0-r0
`, []Package{{"busybox", "1.36.1-r30"}, {"py3-setuptools", "75.1.0-r0"}}},
	}
	for _, c := range cases {
		if got := c.parse(c.raw); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
		if got := c.parse(""); len(got) != 0 {
			t.Errorf("%s: an empty list parses to %v", c.name, got)
		}
	}
}

// touch creates the named files under root, with their directories.
func touch(t *testing.T, root string, names ...string) {
	t.Helper()
	for _, n := range names {
		p := filepath.Join(root, n)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDetect(t *testing.T) {
	cases := []struct {
		files []string
		id    string
		want  string
	}{
		{[]string{"usr/bin/apt-get", "usr/bin/dpkg"}, "debian", "apt"},
		// dnf wins over the yum it replaced and still ships.
		{[]string{"usr/bin/yum", "usr/bin/dnf"}, "fedora", "dnf"},
		{[]string{"usr/bin/yum"}, "centos", "yum"},
		{[]string{"usr/bin/zypper"}, "opensuse-leap", "zypper"},
		{[]string{"usr/bin/pacman"}, "arch", "pacman"},
		{[]string{"sbin/apk"}, "alpine", "apk"},
		// A distribution nobody has heard of is driven by what it has.
		{[]string{"usr/bin/apt-get"}, "somederivative", "apt"},
		{[]string{"usr/bin/xbps-install"}, "void", ""},
	}
	for _, c := range cases {
		root := t.TempDir()
		touch(t, root, c.files...)
		got := ""
		if m := Detect(root, c.id); m != nil {
			got = m.Name
		}
		if got != c.want {
			t.Errorf("Detect(%v) = %q, want %q", c.files, got, c.want)
		}
	}
}

// Tumbleweed is only ever upgraded as a whole; Leap is updated.
func TestZypperVerbFollowsTheRelease(t *testing.T) {
	root := t.TempDir()
	touch(t, root, "usr/bin/zypper")
	for id, verb := range map[string]string{"opensuse-tumbleweed": "dist-upgrade", "opensuse-leap": "update"} {
		s, err := Detect(root, id).Script(Upgrade, "/opt/quasar/storage/host", "/opt/quasar")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(s, "zypper -n "+verb+" --auto-agree-with-licenses") {
			t.Errorf("%s does not %s", id, verb)
		}
	}
}

// Every manager's OS upgrade keeps its hands off Docker, one way or another.
func TestUpgradeLeavesDockerAlone(t *testing.T) {
	for _, m := range []Manager{apt, dnf, yum, zypperManager(false), pacman, apk} {
		s, err := m.Script(Upgrade, "/d", "/opt/quasar")
		if err != nil {
			t.Fatal(err)
		}
		body := m.upgrade
		if !strings.Contains(body, "docker_pkgs") && !strings.Contains(body, "-x 'docker*'") && !strings.Contains(body, dockerRE) {
			t.Errorf("%s upgrades Docker along with the system", m.Name)
		}
		if !strings.Contains(s, body) {
			t.Errorf("%s script does not carry its upgrade", m.Name)
		}
	}
}

func TestScriptRejectsUnknownKind(t *testing.T) {
	if _, err := apt.Script("wipe", "/d", "/opt/quasar"); err == nil {
		t.Fatal("an unknown kind of job was written")
	}
}
