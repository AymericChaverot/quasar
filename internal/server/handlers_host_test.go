package server

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"quasar/internal/config"
	"quasar/internal/db"
	"quasar/internal/docker"
	"quasar/internal/hostupdate"
	"quasar/internal/updater"
	"quasar/internal/vps"
)

// fakeHost lays out a host filesystem with the given files under a temporary
// root, points the dashboard at it, and returns a server reading from it.
func fakeHost(t *testing.T, files ...string) *Server {
	t.Helper()
	root := t.TempDir()
	for _, f := range files {
		p := filepath.Join(root, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("ID=fedora\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOST_ROOT", root)
	t.Setenv("HOST_ETC", filepath.Join(root, "etc"))
	t.Setenv("HOST_PROC", filepath.Join(root, "proc"))
	storage := t.TempDir()
	return &Server{cfg: config.Config{
		HostManagement: true,
		HostRootPath:   root,
		DBPath:         filepath.Join(storage, "database.sqlite"),
		AppsDir:        filepath.Join(storage, "apps"),
	}}
}

// A server that can be managed, with every piece it takes.
var managedHost = []string{"etc/os-release", "run/systemd/system/.keep", "usr/bin/dnf"}

func TestHostManagerSaysWhyNot(t *testing.T) {
	cases := []struct {
		name   string
		files  []string
		off    bool
		engine docker.EngineInfo
		want   string // part of the reason, "" for a host that can be managed
	}{
		{"managed", managedHost, false, docker.EngineInfo{}, ""},
		{"switched off", managedHost, true, docker.EngineInfo{}, "HOST_MANAGEMENT=off"},
		{"not Linux", []string{"run/systemd/system/.keep", "usr/bin/dnf"}, false, docker.EngineInfo{}, "not Linux"},
		{"no systemd", []string{"etc/os-release", "usr/bin/dnf"}, false, docker.EngineInfo{}, "systemd"},
		{"rootless", managedHost, false, docker.EngineInfo{Rootless: true}, "rootless"},
		{"no package manager", []string{"etc/os-release", "run/systemd/system/.keep"}, false, docker.EngineInfo{}, "apt, dnf"},
	}
	for _, c := range cases {
		s := fakeHost(t, c.files...)
		s.cfg.HostManagement = !c.off
		m, reason := s.hostManager(c.engine)
		switch {
		case c.want == "" && (m == nil || reason != ""):
			t.Errorf("%s: not managed: %s", c.name, reason)
		case c.want != "" && (m != nil || !strings.Contains(reason, c.want)):
			t.Errorf("%s: reason %q, want one mentioning %q", c.name, reason, c.want)
		}
	}
}

// The card splits what an OS upgrade installs from what only a Docker upgrade
// does, and names the engine version on offer.
func TestHostUpdateViewSplitsDockerFromTheSystem(t *testing.T) {
	s := fakeHost(t, managedHost...)
	store := s.hostStore()
	if err := os.MkdirAll(store.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	list := "kernel.x86_64  6.11.4-301.fc41  updates\n" +
		"containerd.io.x86_64  1.7.28-1.fc41  docker-ce-stable\n" +
		"docker-ce.x86_64  3:28.5.1-1.fc41  docker-ce-stable\n"
	if err := os.WriteFile(filepath.Join(store.Dir, "updates.txt"), []byte(list), 0o600); err != nil {
		t.Fatal(err)
	}

	v := s.hostUpdateView(docker.EngineInfo{}, vps.HostInfo{Kernel: "6.11.3-300.fc41", Booted: time.Now()}, true)
	if v.Reason != "" || v.Manager != "dnf" || !v.Checked {
		t.Fatalf("view = %+v", v)
	}
	if len(v.System) != 1 || v.System[0].Name != "kernel" {
		t.Errorf("system updates = %v", v.System)
	}
	if len(v.Docker) != 2 || v.Engine != "28.5.1" {
		t.Errorf("docker updates = %v, engine %q", v.Docker, v.Engine)
	}
	if v.Busy() {
		t.Error("busy with no job recorded")
	}
}

func TestUpstreamVersion(t *testing.T) {
	for in, want := range map[string]string{
		"5:28.5.1-1~ubuntu.24.04~noble": "28.5.1",
		"3:28.5.1-1.fc41":               "28.5.1",
		"1:28.5.1-1":                    "28.5.1",
		"28.5.1-r0":                     "28.5.1",
		"28.5.1":                        "28.5.1",
	} {
		if got := upstreamVersion(in); got != want {
			t.Errorf("upstreamVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

// A second job is refused while the first is running, whatever tab asks.
func TestStartHostJobRefusesASecond(t *testing.T) {
	s := fakeHost(t, managedHost...)
	if err := s.hostStore().Begin(hostupdate.Job{Kind: hostupdate.Upgrade, Started: time.Now()}); err != nil {
		t.Fatal(err)
	}
	m := hostupdate.Detect(s.cfg.HostRootPath, "fedora")
	if err := s.startHostJob(m, hostupdate.Check, "admin", nil); !errors.Is(err, errHostBusy) {
		t.Fatalf("second job: %v, want errHostBusy", err)
	}
}

func TestHostJobRejectsUnknownKind(t *testing.T) {
	s := fakeHost(t, managedHost...)
	r := httptest.NewRequest(http.MethodPost, "/system/host/wipe", nil)
	r.SetPathValue("kind", "wipe")
	w := httptest.NewRecorder()
	s.handleHostJob(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", w.Code)
	}
}

func TestDockerOffer(t *testing.T) {
	engine := HostUpdateView{Engine: "28.5.1", Docker: []hostupdate.Package{{Name: "containerd.io", Version: "1.7.28-1"}, {Name: "docker-ce", Version: "3:28.5.1-1.fc41"}}}
	if got := engine.DockerOffer(); got != "Docker 28.5.1" {
		t.Errorf("with the engine: %q", got)
	}
	parts := HostUpdateView{Docker: []hostupdate.Package{{Name: "containerd.io", Version: "1.7.28-1"}}}
	if got := parts.DockerOffer(); got != "containerd.io 1.7.28" {
		t.Errorf("without the engine: %q", got)
	}
	if got := (HostUpdateView{}).DockerOffer(); got != "" {
		t.Errorf("with nothing: %q", got)
	}
}

// Updating Quasar or Traefik waits for a job on the server to finish: a Docker
// upgrade restarts the daemon both of them are driven through.
func TestUpdatesWaitForAHostJob(t *testing.T) {
	s := fakeHost(t, managedHost...)
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	s.db = database
	if err := db.SetSetting(database, updater.SettingLatestTag, "v99.0.0"); err != nil {
		t.Fatal(err)
	}
	if err := s.hostStore().Begin(hostupdate.Job{Kind: hostupdate.Docker, Started: time.Now()}); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	r := withSession(httptest.NewRequest(http.MethodPost, "/system/update/apply", nil))
	s.handleUpdateApply(w, r)
	if n := flashed(s, r); len(n) != 1 || !strings.Contains(n[0].Text, "job is running") {
		t.Fatalf("self-update went ahead under a host job: %d %v", w.Code, n)
	}
	if s.update.state().phase != updateIdle {
		t.Fatal("the self-update was claimed")
	}
}

// Whoever started a job on the server hears how it ended, wherever they are by
// then: the end of a check, and the line that says why an upgrade failed.
func TestAHostJobTellsWhoeverStartedIt(t *testing.T) {
	s := fakeHost(t, managedHost...)
	store := s.hostStore()
	for _, c := range []struct {
		exit, kind, title string
		wantKind          string
	}{
		{"0", "check", "Update check finished", noticeOK},
		{"100", "upgrade", "Package update failed", noticeErr},
	} {
		if err := store.Begin(hostupdate.Job{Kind: hostupdate.Kind(c.kind), Started: time.Now()}); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(store.Dir, "job.log"), []byte("Reading package lists\nE: could not get lock\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(store.Dir, "job.exit"), []byte(c.exit+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		var got []Notice
		s.reportHostJob(hostupdate.Kind(c.kind), func(n Notice) { got = append(got, n) })
		if len(got) != 1 || got[0].Title != c.title || got[0].Kind != c.wantKind {
			t.Errorf("%s: %+v", c.kind, got)
		}
		if c.wantKind == noticeErr && !strings.Contains(got[0].Detail, "could not get lock") {
			t.Errorf("the failure does not say why: %+v", got[0])
		}
	}
}
