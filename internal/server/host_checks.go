package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"quasar/internal/db"
	"quasar/internal/docker"
	"quasar/internal/event"
	"quasar/internal/hostupdate"
	"quasar/internal/notify"
	"quasar/internal/vps"
)

const (
	// hostCheckEvery is how old the list of updates may get before the
	// server is asked again. Package mirrors move daily at most, and a check
	// is not free: it refreshes every repository's metadata.
	hostCheckEvery = 24 * time.Hour
	// hostCheckRetry spaces out attempts at a check that keeps failing — a
	// mirror down for the day — so that it is not retried every hour.
	hostCheckRetry = 6 * time.Hour
	// settingHostNotified fingerprints the updates last announced, so the
	// same list is not announced every day until it is installed.
	settingHostNotified = "host_updates_notified"
)

// StartHostChecks looks for updates on the server once a day, in the
// background, and says so over the configured notifications when there are
// new ones. Nothing is ever installed without somebody pressing the button.
func (s *Server) StartHostChecks() {
	go func() {
		// Off the start-up path, and out of the way of an update that may
		// be what this start is the end of.
		time.Sleep(2 * time.Minute)
		var tried time.Time
		for {
			if time.Since(tried) >= hostCheckRetry && s.checkHostIfDue() {
				tried = time.Now()
			}
			time.Sleep(time.Hour)
		}
	}()
}

// checkHostIfDue runs a check when the last one to work is older than a day,
// waits for it, and announces what it found. It reports whether it tried.
func (s *Server) checkHostIfDue() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	m, _ := s.hostManager(s.dock.EngineInfo(ctx))
	cancel()
	if m == nil {
		return false
	}
	store := s.hostStore()
	if _, checked, ok := store.Updates(m); ok && time.Since(checked) < hostCheckEvery {
		return false
	}
	// Busy is not an attempt: whatever is running will be done by the next
	// hour.
	if err := s.startHostJob(m, hostupdate.Check, db.ActorSystem, nil); err != nil {
		return false
	}
	for store.Status(hostupdate.BootID()).Running {
		time.Sleep(5 * time.Second)
	}
	if st := store.Status(hostupdate.BootID()); st.Done() {
		s.announceHostUpdates(m)
	}
	return true
}

// announceHostUpdates notifies about the updates waiting on the server, once
// per distinct list: the same packages at the same versions are not news the
// second day.
func (s *Server) announceHostUpdates(m *hostupdate.Manager) {
	pkgs, _, _ := s.hostStore().Updates(m)
	if len(pkgs) == 0 {
		return
	}
	fp := fingerprint(pkgs)
	if db.GetSetting(s.db, settingHostNotified) == fp {
		return
	}
	if err := db.SetSetting(s.db, settingHostNotified, fp); err != nil {
		event.Warning("host", "remembering the updates announced", err.Error())
	}

	host := vps.CollectHost()
	view := s.hostUpdateView(docker.EngineInfo{}, host, false)
	event.Info("host", hostUpdatesCount(len(pkgs))+" waiting")
	notify.Send(s.db, hostUpdatesMessage(pkgs, view.Reboot))
}

// hostUpdatesMessage is the notification: how many updates, whether Docker is
// among them, since that one is installed on its own and stops everything, and
// whether a restart is already owed.
func hostUpdatesMessage(pkgs []hostupdate.Package, reboot bool) string {
	msg := "Quasar: " + hostUpdatesCount(len(pkgs)) + " waiting on the server"
	for _, p := range pkgs {
		if hostupdate.IsDocker(p.Name) {
			msg += ", Docker among them — it is installed on its own and restarts every application"
			break
		}
	}
	msg += ". Install them from the System page."
	if reboot {
		msg += " A restart is also needed for what was installed before."
	}
	return msg
}

func hostUpdatesCount(n int) string {
	if n == 1 {
		return "1 update"
	}
	return fmt.Sprintf("%d updates", n)
}

// fingerprint identifies a list of updates, whatever order the package
// manager printed it in.
func fingerprint(pkgs []hostupdate.Package) string {
	lines := make([]string, len(pkgs))
	for i, p := range pkgs {
		lines[i] = p.Name + "=" + p.Version
	}
	sort.Strings(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:])
}
