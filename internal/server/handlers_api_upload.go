package server

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"quasar/internal/db"
	"quasar/internal/docker"
	"quasar/internal/event"
)

// How large a folder upload may be, compressed. The default fits a project's
// source with room to spare; an operator whose projects carry heavy assets
// raises it in Settings.
const (
	defaultUploadLimitMB = 200
	minUploadLimitMB     = 1
	maxUploadLimitMB     = 10240
)

// uploadLimitMB is the configured limit, or the default when none is stored
// or the stored one is out of range.
func (s *Server) uploadLimitMB() int {
	if n, err := strconv.Atoi(db.GetSetting(s.db, db.SettingUploadLimitMB)); err == nil &&
		n >= minUploadLimitMB && n <= maxUploadLimitMB {
		return n
	}
	return defaultUploadLimitMB
}

func (s *Server) uploadLimit() int64 { return int64(s.uploadLimitMB()) << 20 }

// handleAPIUpload takes a folder, as the gzipped tar qsr sends, for an
// application deployed from a local folder: it replaces the application's
// source with it and deploys it. Like a deploy, it answers 202 once the deploy
// has started, and the caller polls the application for how it ends.
//
// The source is only replaced once the whole upload has been unpacked and
// checked, so a refused or interrupted upload leaves the running version, and
// what a redeploy would rebuild, as they were.
func (s *Server) handleAPIUpload(w http.ResponseWriter, r *http.Request) {
	a, err := db.GetApp(s.db, s.keyring, r.PathValue("id"))
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "application not found")
		return
	}
	if a.DeployType != "local" {
		writeAPIError(w, http.StatusConflict,
			a.Name+" is deployed from "+deploySourceName(a.DeployType)+", not from an uploaded folder")
		return
	}
	if d := s.dock.Deploying(a.ID); d != nil && d.Running {
		writeAPIError(w, http.StatusConflict, "a deploy is already in progress")
		return
	}
	limit := s.uploadLimit()
	tooLarge := func() {
		writeAPIError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf(
			"the upload is larger than the %d MB this server accepts; an admin can raise the limit in Settings", s.uploadLimitMB()))
	}
	// Refused before a byte is read when the client says how much is coming,
	// which qsr does: no point in receiving 1 GB to turn it down.
	if r.ContentLength > limit {
		tooLarge()
		return
	}
	stats, err := s.dock.ReplaceSource(a.ID, http.MaxBytesReader(w, r.Body, limit), limit)
	if err != nil {
		var maxBytes *http.MaxBytesError
		var unpacked *docker.UploadTooLargeError
		switch {
		case errors.As(err, &maxBytes):
			tooLarge()
		case errors.As(err, &unpacked):
			writeAPIError(w, http.StatusRequestEntityTooLarge, "the upload unpacks to more than ten times its own size, which no source tree does")
		case errors.Is(err, docker.ErrUploadBusy):
			writeAPIError(w, http.StatusConflict, err.Error())
		default:
			log.Printf("api: upload to %s: %v", a.Name, err)
			writeAPIError(w, http.StatusBadRequest, "the upload was refused: "+err.Error())
		}
		return
	}
	s.dock.UpdateAsync(a, "upload")
	detail := fmt.Sprintf("%d files, %s", stats.Files, humanSize(stats.Bytes))
	s.auditAPI(r, "app.upload", a.Name, detail)
	event.Info("deploy", a.Name, "folder uploaded", detail, "by "+apiActor(apiIdentity(r)))
	writeJSON(w, http.StatusAccepted, map[string]any{
		"status": "deploying", "app": a.ID, "files": stats.Files, "bytes": stats.Bytes,
	})
}

// deploySourceName says where an application of a type gets what it runs.
func deploySourceName(t string) string {
	switch t {
	case "git":
		return "a git repository"
	case "compose":
		return "a compose file"
	default:
		return "an image"
	}
}

// humanSize is a byte count as the audit trail shows it.
func humanSize(n int64) string {
	switch {
	case n >= 1<<20:
		return strconv.FormatFloat(float64(n)/(1<<20), 'f', 1, 64) + " MB"
	case n >= 1<<10:
		return strconv.FormatFloat(float64(n)/(1<<10), 'f', 1, 64) + " KB"
	default:
		return strconv.FormatInt(n, 10) + " bytes"
	}
}

// handleUploadSettings stores the folder upload limit.
func (s *Server) handleUploadSettings(w http.ResponseWriter, r *http.Request) {
	mb, err := strconv.Atoi(strings.TrimSpace(r.FormValue("upload_limit_mb")))
	if err != nil || mb < minUploadLimitMB || mb > maxUploadLimitMB {
		s.redirectSettings(w, r, warnNotice("Upload limit not saved",
			fmt.Sprintf("Between %d and %d MB.", minUploadLimitMB, maxUploadLimitMB)))
		return
	}
	if err := db.SetSetting(s.db, db.SettingUploadLimitMB, strconv.Itoa(mb)); err != nil {
		s.redirectSettings(w, r, errNotice("Upload limit not saved", "", err))
		return
	}
	detail := fmt.Sprintf("%d MB", mb)
	s.audit(r, "settings.uploads", "", detail)
	s.redirectWith(w, r, "/settings#uploads", okNotice("Upload limit saved", "Folders up to "+detail+", compressed."))
}
