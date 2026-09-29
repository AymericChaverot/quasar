package hostupdate

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// MaxRun is how long a job may go without writing its exit code before it is
// taken to have died with nobody left to write it — the unit killed, the
// machine lost power. An upgrade on a slow mirror is the longest there is.
const MaxRun = 3 * time.Hour

// Job is a run on the host, as the dashboard recorded it when it started one.
type Job struct {
	Kind    Kind      `json:"kind"`
	By      string    `json:"by"`
	Started time.Time `json:"started"`
	// BootID is the host's boot at the start: a job whose boot is gone was
	// ended by a restart of the machine, which is the whole point of a
	// reboot and the end of anything else.
	BootID string `json:"boot_id"`
}

// Status is what the last job has got to.
type Status struct {
	Job
	Running     bool
	Exit        int  // the job's exit code, once it has one
	Interrupted bool // it ended without saying how
	Refused     bool // it never reached the host
}

// Done reports whether the job finished and worked.
func (s Status) Done() bool { return s.Kind != "" && !s.Running && !s.Failed() }

// Failed reports whether the job finished and did not work.
func (s Status) Failed() bool {
	return s.Kind != "" && !s.Running && (s.Interrupted || s.Refused || s.Exit != 0)
}

// Store is the directory a job's files live in: the record the dashboard
// writes, and the log, exit code and list of updates the job writes.
type Store struct{ Dir string }

func (s Store) path(name string) string { return filepath.Join(s.Dir, name) }

// Begin records a job about to start, clearing what the previous one left.
// The list of updates stays: it is still the last answer until a check
// replaces it.
func (s Store) Begin(job Job) error {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	for _, name := range []string{"job.exit", "job.log"} {
		if err := os.Remove(s.path(name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	data, err := json.Marshal(job)
	if err != nil {
		return err
	}
	return writeAtomic(s.path("job.json"), data)
}

// refused is what Abort writes in place of an exit code: no script writes it,
// so a job that never ran is not mistaken for one that ran and failed.
const refused = "refused"

// Abort ends a job that never got as far as the host, with the reason in its
// log where the operator will look for it.
func (s Store) Abort(reason string) {
	_ = os.WriteFile(s.path("job.log"), []byte(reason+"\n"), 0o600)
	_ = os.WriteFile(s.path("job.exit"), []byte(refused+"\n"), 0o600)
}

// Status reads what the last job has got to. bootID is the host's boot now.
func (s Store) Status(bootID string) Status {
	var st Status
	data, err := os.ReadFile(s.path("job.json"))
	if err != nil || json.Unmarshal(data, &st.Job) != nil {
		return Status{}
	}
	if raw, err := os.ReadFile(s.path("job.exit")); err == nil {
		word := strings.TrimSpace(string(raw))
		if word == refused {
			st.Refused = true
			return st
		}
		if code, err := strconv.Atoi(word); err == nil {
			st.Exit = code
			return st
		}
		st.Interrupted = true
		return st
	}
	rebooted := st.BootID != "" && bootID != "" && st.BootID != bootID
	switch {
	case rebooted && st.Kind == Reboot:
		// Exactly what was asked for.
	case rebooted, time.Since(st.Started) > MaxRun:
		st.Interrupted = true
	default:
		st.Running = true
	}
	return st
}

// Log is the end of the last job's output: the part that says how it ended.
func (s Store) Log(lines int) string {
	data, err := os.ReadFile(s.path("job.log"))
	if err != nil {
		return ""
	}
	all := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(all) > lines {
		all = all[len(all)-lines:]
	}
	return strings.Join(all, "\n")
}

// Updates is what the last successful check found, and when. ok is false when
// no check has ever worked.
func (s Store) Updates(m *Manager) (pkgs []Package, checked time.Time, ok bool) {
	path := s.path("updates.txt")
	info, err := os.Stat(path)
	if err != nil {
		return nil, time.Time{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, time.Time{}, false
	}
	return m.parse(string(data)), info.ModTime(), true
}

// BootID identifies the host's current boot, read through HOST_PROC like
// everything else gopsutil reads. Empty when it cannot be read.
func BootID() string {
	data, err := os.ReadFile(filepath.Join(envOr("HOST_PROC", "/proc"), "sys", "kernel", "random", "boot_id"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// writeAtomic replaces a file in one step, so a reader never sees half of it.
func writeAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
