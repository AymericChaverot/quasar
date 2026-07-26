package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"quasar/internal/backup"
	"quasar/internal/certs"
	"quasar/internal/db"
	"quasar/internal/docker"
	"quasar/internal/secrets"
	"quasar/internal/updater"
	"quasar/internal/version"
	"quasar/internal/vps"
)

// AppSize pairs an app with its on-disk footprint for the system page.
type AppSize struct {
	Name   string
	ID     string
	SizeMB float64
}

func (s *Server) systemData(r *http.Request) map[string]any {
	latest := db.GetSetting(s.db, updater.SettingLatestTag)
	data := map[string]any{
		"Title":       "System",
		"Backups":     backup.List(s.cfg.BackupsDir),
		"AutoOn":      db.GetSetting(s.db, db.SettingBackupAuto) == "true",
		"Retention":   db.GetSetting(s.db, db.SettingBackupRetention),
		"Current":     version.Version,
		"Latest":      latest,
		"CheckedAt":   db.GetSetting(s.db, updater.SettingCheckedAt),
		"UpdateAvail": updater.IsNewer(version.Version, latest),
		"Repo":        s.cfg.GitHubRepo,
		"Host":        vps.CollectHost(),
		"Engine":      s.dock.EngineInfo(r.Context()),
		"GoRuntime":   runtime.Version(),
	}
	if du, err := s.dock.DiskUsage(r.Context()); err == nil {
		data["Disk"] = du
	}
	acmePath := filepath.Join(s.cfg.HostRootPath, filepath.Dir(s.cfg.AppsDir), "traefik", "acme.json")
	if list, err := certs.Collect(acmePath); err == nil {
		data["Certs"] = list
	}
	if apps, err := db.ListApps(s.db, s.keyring); err == nil {
		var sizes []AppSize
		for _, a := range apps {
			sizes = append(sizes, AppSize{
				Name:   a.Name,
				ID:     a.ID,
				SizeMB: float64(s.dock.AppDirSize(a.ID)) / (1 << 20),
			})
		}
		data["AppSizes"] = sizes
	}
	return data
}

func (s *Server) handleSystem(w http.ResponseWriter, r *http.Request) {
	data := s.systemData(r)
	if msg := r.URL.Query().Get("msg"); msg != "" {
		data["Saved"] = msg
	}
	s.render(w, r, "system", data)
}

func (s *Server) handlePrune(w http.ResponseWriter, r *http.Request) {
	mb, err := s.dock.PruneImages(r.Context())
	if err != nil {
		http.Error(w, "prune failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/system?msg=Pruned dangling images, reclaimed %.0f MB.", mb), http.StatusSeeOther)
}

func (s *Server) handleBackupNow(w http.ResponseWriter, r *http.Request) {
	name, err := backup.Run(s.db, s.keyring, s.cfg.AppsDir, s.cfg.BackupsDir, s.dock.DumpForBackup)
	if err != nil {
		http.Error(w, "backup failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.audit(r, "backup.create", name, "")
	http.Redirect(w, r, "/system?msg=Backup created: "+name, http.StatusSeeOther)
}

func (s *Server) handleBackupDownload(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !backup.ValidName(name) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Disposition", "attachment; filename="+name)
	http.ServeFile(w, r, filepath.Join(s.cfg.BackupsDir, name))
}

func (s *Server) handleBackupDelete(w http.ResponseWriter, r *http.Request) {
	if err := backup.Delete(s.cfg.BackupsDir, r.PathValue("name")); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.audit(r, "backup.delete", r.PathValue("name"), "")
	http.Redirect(w, r, "/system?msg=Backup deleted.", http.StatusSeeOther)
}

// handleBackupRestore puts a backup's database tables, data directories and
// .env files back in place. Apps must be redeployed to pick up the state.
//
// An archive from another install carries rows sealed with that install's
// master key, so its key file can be uploaded alongside; without it every
// restored app's env and compose data would be unreadable.
func (s *Server) handleBackupRestore(w http.ResponseWriter, r *http.Request) {
	archiveKey, err := s.uploadedKey(r)
	if err != nil {
		http.Error(w, "restore failed: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := backup.Restore(s.db, s.cfg.AppsDir, s.cfg.BackupsDir, r.PathValue("name"), s.keyring, archiveKey); err != nil {
		http.Error(w, "restore failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	msg := "Backup restored. Redeploy applications to apply their restored configuration."
	detail := ""
	if archiveKey != nil {
		msg = "Backup restored and re-encrypted with this server's key. Redeploy applications to apply their restored configuration."
		detail = "with an uploaded master key"
	}
	s.audit(r, "backup.restore", r.PathValue("name"), detail)
	http.Redirect(w, r, "/system?msg="+url.QueryEscape(msg), http.StatusSeeOther)
}

// uploadedKey reads the optional master_key file from a restore submission,
// returning nil when the operator did not attach one.
func (s *Server) uploadedKey(r *http.Request) (*secrets.Keyring, error) {
	file, _, err := r.FormFile("master_key")
	if err != nil {
		return nil, nil // no file attached: same-install restore
	}
	defer file.Close()
	// A master key is 32 bytes; the cap is only there so a misselected file
	// can't be read into memory wholesale.
	raw, err := io.ReadAll(io.LimitReader(file, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read master key: %w", err)
	}
	keyring, err := secrets.KeyringFrom(raw)
	if err != nil {
		return nil, fmt.Errorf("%w — upload the master.key file downloaded from the source server", err)
	}
	return keyring, nil
}

// handleMasterKeyDownload serves the at-rest encryption key so it can be kept
// somewhere other than this server.
//
// Backups deliberately exclude this key, which is what makes a leaked archive
// useless on its own — and equally what makes an archive useless on a rebuilt
// server unless the key was saved separately.
func (s *Server) handleMasterKeyDownload(w http.ResponseWriter, r *http.Request) {
	key, err := os.ReadFile(s.cfg.KeyPath)
	if err != nil {
		http.Error(w, "master key unavailable: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// Handing out the key that opens every stored secret is the single most
	// sensitive thing this dashboard can do, so it is always on the record.
	s.audit(r, "master-key.download", "", "")
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="quasar-master.key"`)
	w.Write(key)
}

// handleUpdateCheck queries GitHub for the latest release right now.
func (s *Server) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	latest, err := updater.Check(r.Context(), s.db, s.cfg.GitHubRepo)
	if err != nil {
		http.Redirect(w, r, "/system?msg=Update check failed: "+err.Error(), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/system?msg=Latest release: "+latest, http.StatusSeeOther)
}

// handleUpdateApply launches the self-update to the latest known release.
func (s *Server) handleUpdateApply(w http.ResponseWriter, r *http.Request) {
	latest := db.GetSetting(s.db, updater.SettingLatestTag)
	if latest == "" {
		http.Redirect(w, r, "/system?msg=No release known yet — run a check first.", http.StatusSeeOther)
		return
	}
	imageRef := "ghcr.io/" + strings.ToLower(s.cfg.GitHubRepo) + ":" + latest
	// SelfUpdate pulls the whole image before it can hand off to the updater
	// container, which takes minutes on a modest VPS with no feedback in the
	// browser. Detached from the request context so a reload, a closed tab or
	// an impatient proxy cannot cancel the pull half-way through.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 20*time.Minute)
	defer cancel()
	if err := s.dock.SelfUpdate(ctx, imageRef, s.cfg.SocketNetwork); err != nil {
		http.Error(w, "update failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.audit(r, "platform.update", latest, imageRef)
	http.Redirect(w, r, "/system?msg=Update to "+latest+" started — the dashboard will restart in a few seconds.", http.StatusSeeOther)
}

// getSystemContainer fetches a quasar-* container by name for the read-only
// detail view, 404ing on anything else (including non-system containers).
func (s *Server) getSystemContainer(w http.ResponseWriter, r *http.Request) *docker.SystemContainer {
	sc, err := s.dock.GetSystemContainer(r.Context(), r.PathValue("name"))
	if err != nil {
		http.Error(w, "container not found", http.StatusNotFound)
		return nil
	}
	return &sc
}

// handleSystemContainerDetail shows a read-only detail page for one of
// Quasar's own containers: image, state, live stats and logs, but no
// start/stop/restart/delete actions.
func (s *Server) handleSystemContainerDetail(w http.ResponseWriter, r *http.Request) {
	sc := s.getSystemContainer(w, r)
	if sc == nil {
		return
	}
	s.render(w, r, "system_container_detail", map[string]any{
		"Title":     sc.Name,
		"Container": sc,
	})
}

func (s *Server) handleSystemContainerStatsPartial(w http.ResponseWriter, r *http.Request) {
	sc := s.getSystemContainer(w, r)
	if sc == nil {
		return
	}
	stats, err := s.dock.StatsByName(r.Context(), sc.Name)
	if err != nil {
		s.renderPartial(w, "container_stats_unavailable", nil)
		return
	}
	s.renderPartial(w, "container_stats", stats)
}

func (s *Server) handleBackupSettings(w http.ResponseWriter, r *http.Request) {
	auto := "false"
	if r.FormValue("backup_auto") == "on" {
		auto = "true"
	}
	db.SetSetting(s.db, db.SettingBackupAuto, auto)
	if v := r.FormValue("retention"); v != "" {
		db.SetSetting(s.db, db.SettingBackupRetention, v)
	}
	http.Redirect(w, r, "/system?msg=Backup settings saved.", http.StatusSeeOther)
}
