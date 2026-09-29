package hostupdate

import (
	"fmt"
	"strings"
)

// Kind is what a job on the host does.
type Kind string

const (
	Check   Kind = "check"   // look for updates, change nothing
	Upgrade Kind = "upgrade" // upgrade everything but Docker
	Docker  Kind = "docker"  // upgrade Docker, restarting it and every container
	Reboot  Kind = "reboot"  // restart the server
)

// Valid reports whether k is a kind of job this package can write.
func (k Kind) Valid() bool {
	switch k {
	case Check, Upgrade, Docker, Reboot:
		return true
	}
	return false
}

// Script is the shell a job runs as root on the host. dir is where it leaves
// its log, its exit code and the list of updates — the same path on the host
// and in the dashboard's container, which reads them back. installDir is
// where the system stack's compose project lives, for bringing it back up once
// Docker has restarted.
//
// The job's own work runs in a subshell, so that however it ends — an exit
// deep inside a manager's script included — the exit code is still written:
// that file is what tells a finished job from one still running.
//
// An upgrade ends with a check, so that the count on screen afterwards is what
// is left rather than what there was.
func (m *Manager) Script(kind Kind, dir, installDir string) (string, error) {
	var body string
	switch kind {
	case Check:
		body = m.checkBody()
	case Upgrade:
		// In a subshell of its own: apt's and zypper's end with an exit, once
		// they have released their holds.
		body = "(\n" + m.upgrade + "\n) || exit $?\n" + m.checkBody()
	case Docker:
		body = m.dockerBody(installDir) + "\n" + m.checkBody()
	case Reboot:
		// A moment for the log to reach the disk and for the page that asked
		// to be told, before the machine goes.
		body = "sleep 3\nsystemctl reboot"
	default:
		return "", fmt.Errorf("unknown host job %q", kind)
	}

	var b strings.Builder
	fmt.Fprintf(&b, `set -u
D=%s
export LC_ALL=C DEBIAN_FRONTEND=noninteractive
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
mkdir -p "$D"
rm -f "$D/job.exit"
exec >"$D/job.log" 2>&1

docker_pkgs() { { %s; } 2>/dev/null | grep -E '%s'; }
release_holds() {
	[ -s "$D/held" ] && %s
	rm -f "$D/held"
}

echo "=== $(date -u '+%%Y-%%m-%%dT%%H:%%M:%%SZ') %s with %s ==="
(
%s
)
rc=$?
echo "=== exit $rc ==="
echo $rc >"$D/job.exit"
`, shellQuote(dir), m.installed, dockerRE, m.releaseHolds(), kind, m.Name, body)
	return b.String(), nil
}

// checkBody replaces the list of updates only once the check has worked, so a
// failed one leaves the last good answer on screen rather than an empty one.
func (m *Manager) checkBody() string {
	return m.check + `
[ $? -eq 0 ] || exit 1
mv "$D/updates.tmp" "$D/updates.txt"`
}

// releaseHolds undoes the holds or locks an upgrade placed on the Docker
// packages. Only apt and zypper place any.
func (m *Manager) releaseHolds() string {
	switch m.Name {
	case "apt":
		return `apt-mark unhold $(cat "$D/held") >/dev/null`
	case "zypper":
		return `zypper -n removelock $(cat "$D/held") >/dev/null`
	}
	return "true"
}

// dockerBody upgrades the engine, then makes sure it is running on the new
// version and that the system stack came back with it.
//
// Docker is restarted whatever the package scripts did: some restart it, some
// only reload units, and a daemon still running the old binary would make the
// version on screen a lie. The compose project is brought up afterwards because
// `restart: unless-stopped` only brings back what the daemon remembers, and
// this is the step that decides whether the dashboard reporting all this is
// ever seen again.
func (m *Manager) dockerBody(installDir string) string {
	return `PKGS=$(docker_pkgs | tr '\n' ' ')
if [ -z "$PKGS" ]; then
	echo "No Docker package is installed through ` + m.Name + `: update Docker the way it was installed."
	exit 3
fi
echo "Upgrading: $PKGS"
` + m.docker + ` || exit $?
systemctl daemon-reload
systemctl restart docker || exit 1
n=0
until docker info >/dev/null 2>&1; do
	n=$((n + 1))
	[ $n -ge 60 ] && { echo "Docker did not come back within two minutes"; exit 1; }
	sleep 2
done
docker version --format 'Docker Engine {{.Server.Version}} is running'
cd ` + shellQuote(installDir) + ` && docker compose up -d --no-build || exit 1`
}

// shellQuote makes s a single word for sh, whatever it contains.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
