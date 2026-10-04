package server

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"quasar/internal/docker"
	"quasar/internal/event"
	"quasar/internal/hostupdate"
	"quasar/internal/vps"
)

// hostJobTimeout bounds handing a job to the host, not the job itself: the
// container that does it is gone within a second or two, and the job then
// runs on under systemd for as long as it needs.
const hostJobTimeout = 2 * time.Minute

// hostJobs serialises starting jobs on the host. What is running is on disk,
// read back from the job's own files, because a Docker upgrade restarts this
// process half-way through its job and the new one has to know.
type hostJobs struct{ mu sync.Mutex }

var errHostBusy = errors.New("a job is already running on the server")

// hostStore is where jobs on the host leave their files: beside the database,
// on the volume mounted at the same path on the host and in this container.
func (s *Server) hostStore() hostupdate.Store {
	return hostupdate.Store{Dir: filepath.Join(filepath.Dir(s.cfg.DBPath), "host")}
}

// installDir is the system stack's compose project, which the apps directory
// sits in.
func (s *Server) installDir() string { return filepath.Dir(s.cfg.AppsDir) }

// hostManager is the package manager the host can be updated with, or the
// sentence that says why it cannot be — in which case the Environment card
// says that sentence instead of offering anything.
func (s *Server) hostManager(engine docker.EngineInfo) (*hostupdate.Manager, string) {
	if !s.cfg.HostManagement {
		return nil, "Host management is turned off (HOST_MANAGEMENT=off)."
	}
	id, ok := vps.DistroID()
	if !ok {
		return nil, "The server is not managed from here: it is not Linux, or its filesystem is not mounted where the dashboard expects it."
	}
	if _, err := os.Stat(filepath.Join(s.cfg.HostRootPath, "run", "systemd", "system")); err != nil {
		return nil, "The server does not run systemd, which the dashboard hands its updates to."
	}
	if engine.Rootless {
		return nil, "Docker runs rootless, so the dashboard cannot reach the server itself."
	}
	m := hostupdate.Detect(s.cfg.HostRootPath, id)
	if m == nil {
		return nil, "No package manager the dashboard can drive was found (it knows " + hostupdate.Supported + ")."
	}
	return m, ""
}

// startHostJob records a job and hands it to the host. It returns once the
// job is recorded; handing it over happens behind, and a failure there is
// written into the job's own log, where the card reads it.
func (s *Server) startHostJob(m *hostupdate.Manager, kind hostupdate.Kind, by string) error {
	s.host.mu.Lock()
	defer s.host.mu.Unlock()

	store := s.hostStore()
	boot := hostupdate.BootID()
	if store.Status(boot).Running {
		return errHostBusy
	}
	if updating, routing := s.update.state().phase, s.traefik.state().phase; updating == updatePulling || updating == updateHandoff ||
		routing == traefikPulling || routing == traefikRecreating {
		return errors.New("an update of Quasar or Traefik is running")
	}
	script, err := m.Script(kind, store.Dir, s.installDir())
	if err != nil {
		return err
	}
	if err := store.Begin(hostupdate.Job{Kind: kind, By: by, Started: time.Now(), BootID: boot}); err != nil {
		return err
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), hostJobTimeout)
		defer cancel()
		if err := s.dock.RunOnHost(ctx, script); err != nil {
			store.Abort(err.Error())
		}
		s.reportHostJob(kind)
	}()
	return nil
}

// reportHostJob waits for a job to end and writes how it went to the event
// log. A Docker upgrade restarts this process before its job ends, so that
// one is reported by nobody but the card.
func (s *Server) reportHostJob(kind hostupdate.Kind) {
	store := s.hostStore()
	for {
		st := store.Status(hostupdate.BootID())
		if !st.Running {
			if st.Failed() {
				event.Error("host", string(kind)+" failed", lastLine(store.Log(5)))
			} else {
				event.Info("host", string(kind)+" finished", "in "+event.Duration(time.Since(st.Started)))
			}
			return
		}
		time.Sleep(3 * time.Second)
	}
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, "\n"); i >= 0 {
		return s[i+1:]
	}
	return s
}

// HostUpdateView is what the Environment card says about keeping the server
// up to date: whether it can, what is waiting, and the job in flight or the
// last one's outcome.
type HostUpdateView struct {
	IsAdmin bool
	Reason  string // why the server cannot be managed from here; empty when it can
	Manager string

	Job hostupdate.Status
	Log string

	Checked bool
	Ago     string               // when the last check worked
	System  []hostupdate.Package // updates an OS upgrade installs
	Docker  []hostupdate.Package // the Docker packages, upgraded on their own
	Engine  string               // the Docker Engine version on offer, when the engine itself is among them
	Reboot  bool                 // something installed only a restart applies
}

// Busy reports whether the card should keep polling.
func (v HostUpdateView) Busy() bool { return v.Job.Running }

func (s *Server) hostUpdateView(engine docker.EngineInfo, host vps.HostInfo, isAdmin bool) HostUpdateView {
	v := HostUpdateView{IsAdmin: isAdmin}
	m, reason := s.hostManager(engine)
	if m == nil {
		v.Reason = reason
		return v
	}
	v.Manager = m.Name
	store := s.hostStore()
	v.Job = store.Status(hostupdate.BootID())
	if v.Job.Kind != "" {
		v.Log = store.Log(60)
	}
	pkgs, checked, ok := store.Updates(m)
	v.Checked, v.Ago = ok, humanSince(checked)
	for _, p := range pkgs {
		if hostupdate.IsDocker(p.Name) {
			v.Docker = append(v.Docker, p)
			if v.Engine == "" && isEnginePackage(p.Name) {
				v.Engine = upstreamVersion(p.Version)
			}
		} else {
			v.System = append(v.System, p)
		}
	}
	kernel := host.Kernel
	if kernel == "unknown" {
		kernel = ""
	}
	v.Reboot = !host.Booted.IsZero() && hostupdate.RebootRequired(s.cfg.HostRootPath, kernel, host.Booted)
	return v
}

// isEnginePackage names the package that carries the daemon itself, under the
// names distributions give it.
func isEnginePackage(name string) bool {
	switch name {
	case "docker-ce", "docker.io", "docker", "moby-engine":
		return true
	}
	return false
}

// upstreamVersion is the version Docker itself would give, out of the one a
// package carries: "5:28.5.1-1~ubuntu.24.04~noble" is 28.5.1.
func upstreamVersion(v string) string {
	if _, after, ok := strings.Cut(v, ":"); ok {
		v = after
	}
	if i := strings.IndexAny(v, "-~+"); i > 0 {
		v = v[:i]
	}
	return v
}

// handleHostJob starts a job on the host: a check, an upgrade of the system, an
// upgrade of Docker, or a reboot. Every one of them is audited, and the page
// is sent straight back to the System page, whose Environment card follows
// the job.
func (s *Server) handleHostJob(w http.ResponseWriter, r *http.Request) {
	kind := hostupdate.Kind(r.PathValue("kind"))
	if !kind.Valid() {
		http.NotFound(w, r)
		return
	}
	m, reason := s.hostManager(s.dock.EngineInfo(r.Context()))
	if m == nil {
		s.redirectSystem(w, r, warnNotice("Nothing was started", reason))
		return
	}
	if err := s.startHostJob(m, kind, s.actor(r)); err != nil {
		s.redirectSystem(w, r, errNotice("Nothing was started", "", err))
		return
	}
	s.audit(r, "host."+string(kind), m.Name, "")
	event.Info("host", string(kind)+" started", "with "+m.Name, "by "+s.actor(r))
	s.redirectSystem(w, r, hostStartedMessage[kind])
}

// hostStartedMessage is what the System page says once a job is under way. The
// Environment card follows the job itself, so this names the action rather
// than its progress.
var hostStartedMessage = map[hostupdate.Kind]Notice{
	hostupdate.Check:   infoNotice("Update check started", "Looking for updates on the server."),
	hostupdate.Upgrade: infoNotice("Server update started", "Updating the server's packages. Docker is left alone, so applications keep running."),
	hostupdate.Docker: infoNotice("Docker update started", "Every application and this dashboard stop for a minute or two; "+
		"the page reconnects by itself."),
	hostupdate.Reboot: infoNotice("Server restarting", "Every application and this dashboard are down until it is back."),
}

// JobName is what the card calls the last job.
func (v HostUpdateView) JobName() string {
	switch v.Job.Kind {
	case hostupdate.Check:
		return "Update check"
	case hostupdate.Upgrade:
		return "Package update"
	case hostupdate.Docker:
		return "Docker update"
	case hostupdate.Reboot:
		return "Restart"
	}
	return ""
}

// Ran is how long ago the last job started.
func (v HostUpdateView) Ran() string { return humanSince(v.Job.Started) }

// Pending counts every package waiting, Docker's included.
func (v HostUpdateView) Pending() int { return len(v.System) + len(v.Docker) }

// Failure says how the last job went wrong, to follow "… failed:".
func (v HostUpdateView) Failure() string {
	switch {
	case v.Job.Refused:
		return "it never reached the server"
	case v.Job.Interrupted:
		return "it was cut short before it could finish"
	}
	return "it exited with code " + strconv.Itoa(v.Job.Exit)
}

// DockerOffer names the Docker update on offer: the engine's new version, or,
// when only some other part of it is out of date — the compose plugin,
// containerd — the first of those. Still worth the upgrade, and still a
// restart of Docker.
func (v HostUpdateView) DockerOffer() string {
	if v.Engine != "" {
		return "Docker " + v.Engine
	}
	if len(v.Docker) > 0 {
		return v.Docker[0].Name + " " + upstreamVersion(v.Docker[0].Version)
	}
	return ""
}

// hostJobRunning reports whether a job is running on the server, for the
// updates of Quasar and Traefik, which must not start under one: a Docker
// upgrade restarts the daemon they are talking to.
func (s *Server) hostJobRunning() bool {
	return s.hostStore().Status(hostupdate.BootID()).Running
}
