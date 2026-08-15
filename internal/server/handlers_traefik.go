package server

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"quasar/internal/docker"
	"quasar/internal/updater"
	"quasar/internal/version"
)

// The phases a Traefik update goes through, as the Environment card reads them.
// The zero phase, "", is no run in this process — nothing to report either way.
const (
	traefikPulling    = "pulling"    // transferring the image; the router is untouched
	traefikRecreating = "recreating" // the router is being replaced
	traefikFailed     = "failed"
	traefikDone       = "done"
)

// traefikTimeout bounds the whole run: an image transfer over whatever link the
// VPS has, then three attempts at bringing the router up, then a rollback.
const traefikTimeout = 20 * time.Minute

// traefikRun is the Traefik update in flight, written by the goroutine driving
// it and read by the Environment card while it polls.
//
// In memory and never persisted, like the self-update's: it describes an
// attempt, not a state of the server. What the card reports once a run is over
// is the version Traefik is actually running, which is read from the daemon.
type traefikRun struct {
	mu      sync.Mutex
	phase   string
	target  string
	percent float64
	detail  string
	err     string
}

// begin claims the run, reporting false if one is already under way — two tabs
// on the System page must not start two updates of the same router.
func (t *traefikRun) begin(target string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.phase == traefikPulling || t.phase == traefikRecreating {
		return false
	}
	t.phase, t.target, t.percent, t.detail, t.err = traefikPulling, target, 0, "", ""
	return true
}

func (t *traefikRun) progress(percent float64, detail string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.percent, t.detail = percent, detail
}

// recreating marks the point the pull is done and compose takes over. This is
// where the dashboard stops being reachable for a few seconds, so it is worth
// saying on screen before it happens rather than after.
func (t *traefikRun) recreating() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.phase, t.percent, t.detail = traefikRecreating, 100, ""
}

func (t *traefikRun) finish(err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err != nil {
		t.phase, t.err = traefikFailed, err.Error()
		return
	}
	t.phase, t.err = traefikDone, ""
}

func (t *traefikRun) state() traefikRun {
	t.mu.Lock()
	defer t.mu.Unlock()
	return traefikRun{phase: t.phase, target: t.target, percent: t.percent, detail: t.detail, err: t.err}
}

// TraefikView is the Traefik row of the Environment card: what the router runs,
// what this release of Quasar was tested against, and whatever an update in
// flight has got to.
type TraefikView struct {
	Image     string // what the router is running right now
	Tested    string // the image this Quasar release ships with
	Available bool   // the tested image is newer than the running one
	IsAdmin   bool

	// An update in flight, or the outcome of the last one in this process.
	Phase   string
	Percent float64
	Detail  string
	Err     string
}

// Busy reports whether the card should keep polling. Only a run still going
// changes on its own; an outcome sits there until the page is left.
func (v TraefikView) Busy() bool { return v.Phase == traefikPulling || v.Phase == traefikRecreating }

// traefikView compares what is running against what this release was built for.
//
// running is the image the daemon reports for the router, which is empty when
// it could not be inspected — in which case nothing is offered, because an
// update is only worth proposing when there is something to compare against.
func (s *Server) traefikView(running string, isAdmin bool) TraefikView {
	v := TraefikView{Image: running, Tested: version.TraefikImage, IsAdmin: isAdmin}
	run := s.traefik.state()
	v.Phase, v.Percent, v.Detail, v.Err = run.phase, run.percent, run.detail, run.err
	// Only ever forward. An operator running a Traefik newer than the one this
	// release was tested with made that choice deliberately, and a dashboard
	// offering to take them back a version would be wrong about which of the
	// two knows better.
	v.Available = running != "" && updater.IsNewer(imageTag(running), imageTag(version.TraefikImage))
	return v
}

// imageTag is the version part of an image reference — "traefik:v3.7.10" is
// v3.7.10 — which is the only part two Traefik images differ by here.
//
// The host is dropped before the tag is looked for, because a registry may
// carry a port: the first colon in "registry:5000/traefik:v3.7.10" is not the
// one that introduces the tag.
func imageTag(ref string) string {
	name := ref
	if i := strings.LastIndex(ref, "/"); i >= 0 {
		name = ref[i+1:]
	}
	_, tag, _ := strings.Cut(name, ":")
	return tag
}

// handleTraefikUpdate moves the edge router onto the version this release of
// Quasar was tested with.
//
// The work runs detached from this request for a reason particular to Traefik:
// recreating it takes down the connection this response would travel over. The
// browser is sent back to the System page first, and the Environment card polls
// for the outcome — through a few seconds of failed requests while the router
// is being replaced, which is exactly what it looks like from outside.
func (s *Server) handleTraefikUpdate(w http.ResponseWriter, r *http.Request) {
	running := s.dock.EngineInfo(r.Context()).TraefikImage
	view := s.traefikView(running, true)
	if !view.Available {
		redirectSystem(w, r, "Traefik is already on "+version.TraefikImage+".")
		return
	}
	if !s.traefik.begin(version.TraefikImage) {
		redirectSystem(w, r, "A Traefik update is already running.")
		return
	}
	s.audit(r, "traefik.update", version.TraefikImage, "from "+running)

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), traefikTimeout)
		defer cancel()
		err := s.dock.UpdateTraefik(ctx, version.TraefikImage, func(p docker.TraefikProgress) {
			if p.Recreating {
				s.traefik.recreating()
				return
			}
			s.traefik.progress(p.Pull.Percent, p.Pull.Phase)
		})
		s.traefik.finish(err)
	}()

	redirectSystem(w, r, "Updating Traefik to "+version.TraefikImage+
		". Every site is briefly unavailable while the router restarts, including this page.")
}
