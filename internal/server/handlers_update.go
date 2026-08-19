package server

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"quasar/internal/db"
	"quasar/internal/docker"
	"quasar/internal/updater"
	"quasar/internal/version"
)

// The phases a self-update goes through, as the waiting page reads them.
const (
	updateIdle    = ""        // nothing running in this process
	updatePulling = "pulling" // transferring the new image; the dashboard is still up
	updateHandoff = "handoff" // the updater container is recreating this one
	updateFailed  = "failed"  // never got as far as the handoff
)

// updateTimeout bounds the pull. It is generous because the image is pulled
// whole over whatever link the VPS has.
const updateTimeout = 20 * time.Minute

// updateRun is the progress of the self-update in flight, written by the
// goroutine driving it and read by the status endpoint the waiting page polls.
//
// Deliberately in memory and never persisted: a run ends by replacing this very
// process, so there is no "afterwards" for it to be read in. What the new
// container reports is the version it came up on, which is all the page needs to
// tell success from a rollback.
type updateRun struct {
	mu      sync.Mutex
	phase   string
	target  string  // the release being installed
	percent float64 // of the image transfer
	detail  string  // the daemon's own word for what it is doing
	err     string
}

// begin claims the run, reporting false if one is already under way — two tabs
// submitting the form must not start two pulls of the same image.
func (u *updateRun) begin(target string) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.phase == updatePulling || u.phase == updateHandoff {
		return false
	}
	u.phase, u.target, u.percent, u.detail, u.err = updatePulling, target, 0, "", ""
	return true
}

func (u *updateRun) progress(percent float64, detail string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.percent, u.detail = percent, detail
}

func (u *updateRun) handoff() {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.phase, u.percent, u.detail = updateHandoff, 100, ""
}

func (u *updateRun) fail(msg string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.phase, u.err = updateFailed, msg
}

// state is a consistent copy of the run, for a reader that must not hold the
// lock while it writes a response.
func (u *updateRun) state() updateRun {
	u.mu.Lock()
	defer u.mu.Unlock()
	return updateRun{phase: u.phase, target: u.target, percent: u.percent, detail: u.detail, err: u.err}
}

// updateCardData is what the software-update card renders from. Both the System
// page and the check that swaps the card need exactly these keys, and a card
// swapped in with one of them missing would silently lose a button.
func (s *Server) updateCardData() map[string]any {
	latest := db.GetSetting(s.db, updater.SettingLatestTag)
	return map[string]any{
		"Current":     version.Version,
		"Latest":      latest,
		"CheckedAt":   humanCheckedAt(db.GetSetting(s.db, updater.SettingCheckedAt)),
		"UpdateAvail": updater.IsNewer(version.Version, latest),
		"Repo":        s.cfg.GitHubRepo,
		"CheckEvery":  humanInterval(updater.CheckInterval),
	}
}

// humanInterval names a check cadence the way the card says it out loud, so the
// sentence on the System page follows the constant instead of being a second
// place the frequency is written down.
func humanInterval(d time.Duration) string {
	switch h := int(d.Hours()); {
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + " minutes"
	case h == 1:
		return "hour"
	default:
		return strconv.Itoa(h) + " hours"
	}
}

// updateBadgeData is what the header's update button renders from. It is read
// on every page render and on every poll of the badge, so it stays a settings
// lookup — the network check behind those settings is the background checker's
// job, never a page's.
func (s *Server) updateBadgeData(isAdmin bool) map[string]any {
	latest := db.GetSetting(s.db, updater.SettingLatestTag)
	return map[string]any{
		"IsAdmin":     isAdmin,
		"Latest":      latest,
		"UpdateAvail": updater.IsNewer(version.Version, latest),
	}
}

// handleUpdateBadgePartial refreshes the header button on its own. Without it a
// release found by the checker would only surface on the next full page load,
// which on a dashboard left open on one screen may be hours away.
func (s *Server) handleUpdateBadgePartial(w http.ResponseWriter, r *http.Request) {
	s.renderPartial(w, "update_badge", s.updateBadgeData(true))
}

// humanCheckedAt turns the stored timestamp into the only thing anyone reads it
// for: how stale the answer on screen is. An unparseable or absent value yields
// nothing rather than a fabricated date.
func humanCheckedAt(stored string) string {
	t, err := time.Parse(time.RFC3339, stored)
	if err != nil {
		return ""
	}
	switch d := time.Since(t); {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + " min ago"
	case d < 24*time.Hour:
		return strconv.Itoa(int(d.Hours())) + "h ago"
	default:
		return t.Format("2006-01-02")
	}
}

// handleUpdateCheck queries GitHub for the latest release right now.
//
// htmx swaps the card with the answer, which is what lets the button report
// that it is working — a check against a slow GitHub takes seconds, and the
// full-page redirect it used to do gave nothing back until it landed. A
// submission without htmx still gets that redirect.
func (s *Server) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	latest, err := updater.Check(r.Context(), s.db, s.cfg.GitHubRepo)

	var msg, problem string
	switch {
	case err != nil:
		problem = "Update check failed: " + err.Error()
	case version.Version == "dev":
		// A dev build has no version to compare against, so it is never offered
		// an update; saying "up to date" would be a lie in both directions.
		msg = "Latest release: " + latest + ". This build reports itself as dev, so no update is offered."
	case updater.IsNewer(version.Version, latest):
		msg = latest + " is available."
	default:
		msg = "Up to date — " + version.Version + " is the latest release."
	}

	if r.Header.Get("HX-Request") == "true" {
		data := s.updateCardData()
		data["IsAdmin"] = true // the route is admin-gated; nothing else reaches it
		data["Checked"], data["CheckError"] = msg, problem
		s.renderPartial(w, "update_card", data)
		return
	}
	if problem != "" {
		redirectSystem(w, r, problem)
		return
	}
	redirectSystem(w, r, msg)
}

// handleUpdateApply starts the self-update to the latest known release and
// hands the browser to the page that watches it.
//
// The work runs detached rather than inside this request. Pulling the image
// takes minutes on a modest VPS, and the request cannot outlive it anyway: the
// updater container recreates this very container, so the response the browser
// was waiting on dies with the process and arrives as a gateway error. Which is
// exactly what it used to do.
func (s *Server) handleUpdateApply(w http.ResponseWriter, r *http.Request) {
	latest := db.GetSetting(s.db, updater.SettingLatestTag)
	if latest == "" {
		redirectSystem(w, r, "No release known yet — run a check first.")
		return
	}
	imageRef := "ghcr.io/" + strings.ToLower(s.cfg.GitHubRepo) + ":" + latest
	if !s.update.begin(latest) {
		// Already running: join the run in progress instead of starting a
		// second pull of the same image.
		http.Redirect(w, r, "/system/updating", http.StatusSeeOther)
		return
	}
	s.audit(r, "platform.update", latest, imageRef)

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), updateTimeout)
		defer cancel()
		if err := s.dock.SelfUpdate(ctx, imageRef, s.cfg.SocketNetwork, func(p docker.PullStatus) {
			s.update.progress(p.Percent, p.Phase)
		}); err != nil {
			s.update.fail(err.Error())
			return
		}
		s.update.handoff()
	}()

	http.Redirect(w, r, "/system/updating", http.StatusSeeOther)
}

// handleUpdating shows the page that waits out an update: the transfer, the
// restart, and the reconnection to whatever comes back.
func (s *Server) handleUpdating(w http.ResponseWriter, r *http.Request) {
	run := s.update.state()
	if run.phase == updateIdle {
		// A bookmarked URL, or this page reloaded inside the new container once
		// the update already landed — either way there is nothing to watch.
		redirectSystem(w, r, "No update is running. The dashboard is on "+version.Version+".")
		return
	}
	s.render(w, r, "updating", map[string]any{
		"Title":   "Updating",
		"HideNav": true,
		"Target":  run.target,
		"Current": version.Version,
	})
}

// handleUpdateStatus is what the waiting page polls. It answers with the
// version this process is running as much as with the progress: once the
// container has been replaced, a version that is no longer the one the page was
// served by is the proof the update landed.
func (s *Server) handleUpdateStatus(w http.ResponseWriter, r *http.Request) {
	run := s.update.state()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	// The header is already out, so a broken pipe has nowhere to be reported
	// but the log.
	if err := json.NewEncoder(w).Encode(map[string]any{
		"phase":   run.phase,
		"target":  run.target,
		"percent": run.percent,
		"detail":  run.detail,
		"error":   run.err,
		"version": version.Version,
	}); err != nil {
		log.Printf("update status: writing the response body: %v", err)
	}
}
