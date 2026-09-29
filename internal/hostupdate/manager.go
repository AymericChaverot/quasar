// Package hostupdate knows how to keep the server itself up to date: which
// package manager it has, what a check or an upgrade is in that manager's own
// words, and what the run it started has got to.
//
// Nothing here runs anything. It writes the shell a job runs on the host and
// reads back the files that job leaves behind; starting the job is the Docker
// client's business, deciding whether to is the server's.
package hostupdate

import (
	"os"
	"path/filepath"
	"strings"
)

// Manager is one package manager, as far as Quasar needs to drive it: how to
// ask it what is out of date, how to upgrade the system without Docker, and
// how to upgrade Docker on its own.
type Manager struct {
	Name string // the command, as the operator knows it: "apt", "dnf"…

	// installed lists the installed package names, one per line.
	installed string
	// check refreshes the metadata and writes what is out of date to
	// "$D/updates.tmp", in the manager's own format. Exits non-zero only when
	// the check itself failed.
	check string
	// upgrade upgrades everything except the Docker packages.
	upgrade string
	// docker upgrades the Docker packages and nothing else, where the manager
	// allows it. Every Docker package installed is in $PKGS.
	docker string
	// parse reads what check wrote.
	parse func(raw string) []Package
}

// Package is one package with a newer version available.
type Package struct {
	Name    string
	Version string // the version on offer
}

// IsDocker reports whether a package belongs to the container engine: the
// daemon, its CLI and plugins, and the runtime underneath. An OS upgrade leaves
// them alone, because upgrading any of them restarts Docker and with it every
// application on the server.
func IsDocker(name string) bool {
	for _, p := range []string{"docker", "containerd", "moby", "runc"} {
		if rest, ok := strings.CutPrefix(name, p); ok && (rest == "" || strings.ContainsAny(rest[:1], ".-_")) {
			return true
		}
	}
	return false
}

// dockerRE is IsDocker as the scripts say it.
const dockerRE = `^(docker|containerd|moby|runc)([._-]|$)`

// aptGet runs non-interactively, keeps a configuration file the operator
// changed rather than stopping to ask about it, and waits for the lock
// unattended-upgrades may be holding instead of failing on it.
const aptGet = `apt-get -y -q -o DPkg::Lock::Timeout=300 -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold`

var apt = Manager{
	Name:      "apt",
	installed: `dpkg-query -W -f='${db:Status-Abbrev}${Package}\n' | sed -n 's/^ii *//p'`,
	check: aptGet + ` update || exit 1
apt list --upgradable >"$D/updates.tmp" 2>/dev/null`,
	// apt has no exclude. The Docker packages are held for the run, and only
	// those this run held are released again: a hold the operator placed
	// is theirs. The names are written down as they are held, so a run cut
	// short still has them released by the next one.
	upgrade: aptGet + ` update || exit 1
release_holds
held=$(apt-mark showhold)
for p in $(docker_pkgs); do
	printf '%s\n' "$held" | grep -qx "$p" && continue
	apt-mark hold "$p" >/dev/null && echo "$p" >>"$D/held"
done
` + aptGet + ` upgrade --with-new-pkgs
rc=$?
release_holds
exit $rc`,
	docker: aptGet + ` update || exit 1
` + aptGet + ` install --only-upgrade $PKGS`,
	parse: parseApt,
}

// dnf and yum share their output and their exit codes; check-update exits 100
// when there is something to update.
func rpmManager(name, upgrade, refresh string) Manager {
	return Manager{
		Name:      name,
		installed: `rpm -qa --qf '%{NAME}\n'`,
		check: name + ` -q -y check-update` + refresh + ` >"$D/updates.tmp"
rc=$?
[ $rc -eq 0 ] || [ $rc -eq 100 ] || exit $rc`,
		upgrade: name + ` -y ` + upgrade + refresh + ` -x 'docker*' -x 'containerd*' -x 'moby*' -x runc`,
		docker:  name + ` -y ` + upgrade + refresh + ` $PKGS`,
		parse:   parseRPM,
	}
}

var (
	dnf = rpmManager("dnf", "upgrade", " --refresh")
	yum = rpmManager("yum", "update", "")
)

// zypper has no exclude either, so the Docker packages are locked for the run,
// with the same bookkeeping as apt's holds.
func zypperManager(rolling bool) Manager {
	// A rolling release is only ever upgraded as a whole distribution;
	// `zypper update` on Tumbleweed leaves it half-way between two snapshots.
	verb := "update"
	if rolling {
		verb = "dist-upgrade"
	}
	return Manager{
		Name:      "zypper",
		installed: `rpm -qa --qf '%{NAME}\n'`,
		check: `zypper -n refresh || exit 1
zypper -n list-updates >"$D/updates.tmp"`,
		upgrade: `zypper -n refresh || exit 1
release_holds
locked=$(zypper -n locks)
for p in $(docker_pkgs); do
	printf '%s\n' "$locked" | grep -qw -- "$p" && continue
	zypper -n addlock "$p" >/dev/null && echo "$p" >>"$D/held"
done
zypper -n ` + verb + ` --auto-agree-with-licenses
rc=$?
release_holds
exit $rc`,
		docker: `zypper -n refresh || exit 1
zypper -n update --auto-agree-with-licenses $PKGS`,
		parse: parseZypper,
	}
}

// Arch does not support upgrading part of the system, so Docker is ignored
// during an OS upgrade but can only be upgraded along with everything else.
var pacman = Manager{
	Name:      "pacman",
	installed: `pacman -Qq`,
	// checkupdates, from pacman-contrib, looks without touching the real
	// sync database; `pacman -Sy` alone is the fallback. It exits 2 when
	// there is nothing to update.
	check: `if command -v checkupdates >/dev/null; then
	checkupdates >"$D/updates.tmp"
	rc=$?
	[ $rc -eq 0 ] || [ $rc -eq 2 ] || exit $rc
else
	pacman -Sy || exit 1
	pacman -Qu >"$D/updates.tmp"
fi
true`,
	upgrade: `ignore=$(docker_pkgs | tr '\n' ',' | sed 's/,$//')
pacman -Syu --noconfirm ${ignore:+--ignore "$ignore"}`,
	docker: `pacman -Syu --noconfirm`,
	parse:  parsePacman,
}

// apk has no exclude, but upgrades exactly the packages it is given.
var apk = Manager{
	Name:      "apk",
	installed: `apk info -q`,
	check: `apk update -q || exit 1
apk version -l '<' >"$D/updates.tmp"`,
	upgrade: `apk update -q || exit 1
PKGS=$(apk version -l '<' | awk '$2 == "<" { print $1 }' | sed -E 's/-[^-]+-r[0-9]+$//' | grep -vE "` + dockerRE + `")
[ -z "$PKGS" ] || apk upgrade $PKGS`,
	docker: `apk update -q || exit 1
apk upgrade $PKGS`,
	parse: parseApk,
}

// Detect finds the package manager the host has, by looking for its binary
// under hostRoot rather than by trusting the distribution's name: a derivative
// nobody here has heard of still has one of these. The first found wins, in an
// order that puts the one a distribution actually uses first where several are
// installed — dnf before yum, which it replaced and often still ships as a
// link to.
//
// distroID is only consulted to tell a rolling openSUSE from a fixed release.
// Nil means the host has none that Quasar knows how to drive.
func Detect(hostRoot, distroID string) *Manager {
	candidates := []struct {
		binary string
		m      Manager
	}{
		{"apt-get", apt},
		{"dnf", dnf},
		{"yum", yum},
		{"zypper", zypperManager(strings.Contains(distroID, "tumbleweed") || strings.Contains(distroID, "slowroll"))},
		{"pacman", pacman},
		{"apk", apk},
	}
	for _, c := range candidates {
		for _, dir := range []string{"usr/bin", "usr/sbin", "bin", "sbin"} {
			// Lstat, not Stat: /usr/bin/yum is often an absolute symlink to
			// dnf, which resolves inside this container rather than the host.
			if _, err := os.Lstat(filepath.Join(hostRoot, dir, c.binary)); err == nil {
				m := c.m
				return &m
			}
		}
	}
	return nil
}

// Supported names the package managers Detect knows, for the sentence that
// says a host has none of them.
const Supported = "apt, dnf, yum, zypper, pacman and apk"
