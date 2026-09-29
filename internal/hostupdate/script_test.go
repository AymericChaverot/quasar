package hostupdate

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// sh finds a POSIX shell, or skips: the scripts are run for real below, not
// only read.
func sh(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh to run the job scripts with")
	}
	return p
}

// Every script written for every manager is valid shell.
func TestScriptsParse(t *testing.T) {
	shell := sh(t)
	for _, m := range []Manager{apt, dnf, yum, zypperManager(false), zypperManager(true), pacman, apk} {
		for _, k := range []Kind{Check, Upgrade, Docker, Reboot} {
			s, err := m.Script(k, "/opt/quasar/storage/host", "/opt/qu'asar")
			if err != nil {
				t.Fatal(err)
			}
			if out, err := exec.Command(shell, "-n", "-c", s).CombinedOutput(); err != nil {
				t.Errorf("%s %s: %v\n%s", m.Name, k, err, out)
			}
		}
	}
}

// fake is a manager whose commands only write files, so the envelope around
// them can be run here.
func fake(check, upgrade string) Manager {
	return Manager{Name: "fake", installed: "true", check: check, upgrade: upgrade, docker: "true", parse: parsePacman}
}

func run(t *testing.T, m Manager, kind Kind, dir string) {
	t.Helper()
	if err := (Store{Dir: dir}).Begin(Job{Kind: kind, Started: time.Now()}); err != nil {
		t.Fatal(err)
	}
	s, err := m.Script(kind, filepath.ToSlash(dir), "/nowhere")
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(sh(t), "-c", s).CombinedOutput(); err != nil {
		t.Fatalf("%s: %v\n%s", kind, err, out)
	}
}

func TestCheckRecordsTheUpdates(t *testing.T) {
	dir := t.TempDir()
	store := Store{Dir: dir}
	m := fake(`echo 'linux 6.11.3-1 -> 6.11.4-1' >"$D/updates.tmp"`, "true")
	run(t, m, Check, dir)

	st := store.Status("")
	if !st.Done() {
		t.Fatalf("status after a check that worked: %+v\n%s", st, store.Log(20))
	}
	pkgs, _, ok := store.Updates(&m)
	if !ok || !reflect.DeepEqual(pkgs, []Package{{"linux", "6.11.4-1"}}) {
		t.Fatalf("updates = %v, %v", pkgs, ok)
	}
}

// A check that fails keeps the last good list, and says it failed.
func TestFailedCheckKeepsTheLastList(t *testing.T) {
	dir := t.TempDir()
	store := Store{Dir: dir}
	run(t, fake(`echo 'linux 1 -> 2' >"$D/updates.tmp"`, "true"), Check, dir)
	m := fake(`echo 'mirror unreachable'; false`, "true")
	run(t, m, Check, dir)

	if st := store.Status(""); !st.Failed() || st.Exit != 1 {
		t.Fatalf("status after a failed check: %+v", st)
	}
	if pkgs, _, _ := store.Updates(&m); len(pkgs) != 1 {
		t.Fatalf("the last good list was lost: %v", pkgs)
	}
}

// An upgrade that ends in an exit of its own — apt's and zypper's do, once
// their holds are released — still goes on to the check, and one that fails
// stops before it with its own exit code.
func TestUpgradeExitCodes(t *testing.T) {
	dir := t.TempDir()
	store := Store{Dir: dir}
	check := `echo checked >"$D/checked"; : >"$D/updates.tmp"`

	run(t, fake(check, "exit 0"), Upgrade, dir)
	if st := store.Status(""); st.Exit != 0 {
		t.Fatalf("exit after an upgrade that worked: %d", st.Exit)
	}
	if _, err := os.Stat(filepath.Join(dir, "checked")); err != nil {
		t.Fatal("the upgrade was not followed by a check")
	}

	_ = os.Remove(filepath.Join(dir, "checked"))
	run(t, fake(check, "exit 7"), Upgrade, dir)
	if st := store.Status(""); st.Exit != 7 {
		t.Fatalf("exit after an upgrade that failed: %d, want 7", st.Exit)
	}
	if _, err := os.Stat(filepath.Join(dir, "checked")); err == nil {
		t.Fatal("a failed upgrade went on to the check")
	}
}
