package server

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"quasar/internal/backup"
	"quasar/internal/db"
	"quasar/internal/updater"
	"quasar/internal/version"
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
	}
	if du, err := s.dock.DiskUsage(r.Context()); err == nil {
		data["Disk"] = du
	}
	if apps, err := db.ListApps(s.db); err == nil {
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
	name, err := backup.Run(s.db, s.cfg.AppsDir, s.cfg.BackupsDir)
	if err != nil {
		http.Error(w, "backup failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
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
	http.Redirect(w, r, "/system?msg=Backup deleted.", http.StatusSeeOther)
}

// handleBackupRestore puts a backup's database tables, data directories and
// .env files back in place. Apps must be redeployed to pick up the state.
func (s *Server) handleBackupRestore(w http.ResponseWriter, r *http.Request) {
	if err := backup.Restore(s.db, s.cfg.AppsDir, s.cfg.BackupsDir, r.PathValue("name")); err != nil {
		http.Error(w, "restore failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/system?msg=Backup restored. Redeploy applications to apply their restored configuration.", http.StatusSeeOther)
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
	if err := s.dock.SelfUpdate(r.Context(), imageRef, s.cfg.SocketNetwork); err != nil {
		http.Error(w, "update failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/system?msg=Update to "+latest+" started — the dashboard will restart in a few seconds.", http.StatusSeeOther)
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
