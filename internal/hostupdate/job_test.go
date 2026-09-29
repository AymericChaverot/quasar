package hostupdate

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStatus(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name    string
		job     Job
		exit    string // the exit file's contents, "" for none
		bootNow string
		want    Status
	}{
		{"still going", Job{Kind: Upgrade, Started: now, BootID: "a"}, "", "a",
			Status{Running: true}},
		{"finished", Job{Kind: Upgrade, Started: now, BootID: "a"}, "0\n", "a",
			Status{}},
		{"failed", Job{Kind: Upgrade, Started: now, BootID: "a"}, "100\n", "a",
			Status{Exit: 100}},
		{"cut short by a restart", Job{Kind: Upgrade, Started: now, BootID: "a"}, "", "b",
			Status{Interrupted: true}},
		{"a reboot that happened", Job{Kind: Reboot, Started: now, BootID: "a"}, "", "b",
			Status{}},
		{"a reboot still coming", Job{Kind: Reboot, Started: now, BootID: "a"}, "", "a",
			Status{Running: true}},
		{"silent for too long", Job{Kind: Check, Started: now.Add(-MaxRun - time.Minute)}, "", "",
			Status{Interrupted: true}},
		{"garbled exit", Job{Kind: Check, Started: now}, "zz", "",
			Status{Interrupted: true}},
	}
	for _, c := range cases {
		store := Store{Dir: t.TempDir()}
		if err := store.Begin(c.job); err != nil {
			t.Fatal(err)
		}
		if c.exit != "" {
			if err := os.WriteFile(filepath.Join(store.Dir, "job.exit"), []byte(c.exit), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		got := store.Status(c.bootNow)
		if got.Running != c.want.Running || got.Exit != c.want.Exit || got.Interrupted != c.want.Interrupted {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
		if got.Kind != c.job.Kind {
			t.Errorf("%s: kind %q, want %q", c.name, got.Kind, c.job.Kind)
		}
	}
}

func TestStatusWithNoJob(t *testing.T) {
	st := (Store{Dir: t.TempDir()}).Status("a")
	if st.Kind != "" || st.Running || st.Done() || st.Failed() {
		t.Fatalf("no job reads as %+v", st)
	}
}

// Begin clears what the last job left, except the list of updates, which is
// still the last answer until a check replaces it.
func TestBeginClearsTheLastRun(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	for _, name := range []string{"job.exit", "job.log", "updates.txt"} {
		if err := os.WriteFile(filepath.Join(store.Dir, name), []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Begin(Job{Kind: Check, Started: time.Now()}); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]bool{"job.exit": false, "job.log": false, "updates.txt": true} {
		_, err := os.Stat(filepath.Join(store.Dir, name))
		if (err == nil) != want {
			t.Errorf("%s kept = %v, want %v", name, err == nil, want)
		}
	}
}

func TestAbort(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	if err := store.Begin(Job{Kind: Docker, Started: time.Now()}); err != nil {
		t.Fatal(err)
	}
	store.Abort("could not reach the Docker daemon")
	if st := store.Status(""); !st.Failed() {
		t.Fatalf("an aborted job reads as %+v", st)
	}
	if got := store.Log(5); got != "could not reach the Docker daemon" {
		t.Fatalf("log = %q", got)
	}
}

func TestLogKeepsTheEnd(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	if err := os.WriteFile(filepath.Join(store.Dir, "job.log"), []byte("1\n2\n3\n4\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := store.Log(2); got != "3\n4" {
		t.Fatalf("Log(2) = %q", got)
	}
}
