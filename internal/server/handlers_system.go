package server

import (
	"context"
	"database/sql"
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
	"quasar/internal/offsite"
	"quasar/internal/secrets"
	"quasar/internal/vps"
)

// AppSize pairs an app with its on-disk footprint for the system page.
type AppSize struct {
	Name   string
	ID     string
	SizeMB float64
}

func (s *Server) systemData(r *http.Request) map[string]any {
	data := map[string]any{
		"Title":     "System",
		"Backups":   backup.List(s.cfg.BackupsDir),
		"AutoOn":    db.GetSetting(s.db, db.SettingBackupAuto) == "true",
		"Retention": db.GetSetting(s.db, db.SettingBackupRetention),
		"Host":      vps.CollectHost(),
		"Hardware":  vps.CollectHardware(s.cfg.HostRootPath),
		"Offsite":   offsiteView(s.db),
		"Engine":    s.dock.EngineInfo(r.Context()),
		"GoRuntime": runtime.Version(),
	}
	// The same keys the card is swapped in with after a check, so the page and
	// the swap cannot drift apart.
	for k, v := range s.updateCardData() {
		data[k] = v
	}
	_, certsWritable := s.acmePath()
	data["Certs"] = s.certViews()
	data["CertsWritable"] = certsWritable

	if apps, err := db.ListApps(s.db, s.keyring); err == nil {
		var sizes []AppSize
		ids := make([]string, 0, len(apps))
		for _, a := range apps {
			ids = append(ids, a.ID)
			sizes = append(sizes, AppSize{
				Name:   a.Name,
				ID:     a.ID,
				SizeMB: float64(s.dock.AppDirSize(a.ID)) / (1 << 20),
			})
		}
		data["AppSizes"] = sizes
		// The app list is what tells the scan which images, containers and
		// networks still belong to something, so the storage figures are only
		// asked for once it has been read successfully.
		if st, err := s.dock.Storage(r.Context(), ids); err == nil {
			data["Disk"] = st.Usage
			data["Cleanup"] = st.Cleanup
		}
	}
	return data
}

// appIDs lists the applications the platform still knows about — the set a
// cleanup treats as off limits.
//
// The error is never swallowed by callers. An unreadable app table would come
// back as an empty list, which a sweep would read as "nothing here belongs to
// anyone" and act on.
func (s *Server) appIDs() ([]string, error) {
	apps, err := db.ListApps(s.db, s.keyring)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(apps))
	for _, a := range apps {
		ids = append(ids, a.ID)
	}
	return ids, nil
}

// acmePath resolves Traefik's ACME store and reports whether it can be written
// to. Its directory is bind-mounted read-write into the dashboard on current
// installs; an install predating that mount only reaches the file through the
// read-only mount of the host filesystem, which is enough to list certificates
// but not to delete one.
//
// Writability is probed on the directory rather than on the file itself: the
// file may not exist yet (no certificate obtained) even on a fully writable
// mount, and checking the directory avoids a false read-only result in that
// case.
func (s *Server) acmePath() (path string, writable bool) {
	direct := filepath.Join(s.cfg.TraefikDir, "acme.json")
	if dirWritable(s.cfg.TraefikDir) {
		return direct, true
	}
	return filepath.Join(s.cfg.HostRootPath, filepath.Dir(s.cfg.AppsDir), "traefik", "acme.json"), false
}

// dirWritable reports whether dir can be written to by creating and immediately
// removing a probe file. It returns false for any error, including when the
// directory does not exist.
func dirWritable(dir string) bool {
	f, err := os.CreateTemp(dir, ".writable-probe-*")
	if err != nil {
		return false
	}
	f.Close()
	os.Remove(f.Name())
	return true
}

// heldCerts is what Traefik has actually obtained. An unreadable store yields
// nothing rather than an error: it just means no certificate exists yet, which
// is exactly what the pages using this need to show.
func (s *Server) heldCerts() []certs.Cert {
	path, _ := s.acmePath()
	list, _ := certs.Collect(path)
	return list
}

// CertView pairs a certificate with whatever still claims one of its names,
// which is what decides whether it may be deleted.
type CertView struct {
	certs.Cert
	UsedBy string // application name, or "this dashboard"; empty when nothing routes it
}

// certViews matches every stored certificate against the hostnames the
// platform currently routes. A certificate no host maps to is left over from a
// deleted app or a renamed domain, and only costs space and renewal attempts.
func (s *Server) certViews() []CertView {
	apps, _ := db.ListApps(s.db, s.keyring)
	return matchCerts(s.heldCerts(), s.routedHosts(apps))
}

// routedHosts maps every hostname the platform answers on to what claims it.
func (s *Server) routedHosts(apps []*db.App) map[string]string {
	routed := map[string]string{strings.ToLower("admin." + s.cfg.Domain): "this dashboard"}
	for _, a := range apps {
		for _, host := range append([]string{appHost(a, s.cfg.Domain)}, a.CustomDomainList()...) {
			routed[strings.ToLower(host)] = a.Name
		}
	}
	return routed
}

func matchCerts(held []certs.Cert, routed map[string]string) []CertView {
	views := make([]CertView, 0, len(held))
	for _, c := range held {
		v := CertView{Cert: c}
		// A SAN counts as much as the main domain: the certificate is what a
		// live host is being served with either way.
		for _, name := range append([]string{c.Domain}, c.SANs...) {
			if who, ok := routed[strings.ToLower(name)]; ok {
				v.UsedBy = who
				break
			}
		}
		views = append(views, v)
	}
	return views
}

// handleCertDelete drops a certificate from Traefik's ACME store.
//
// It refuses while something still routes one of the certificate's names:
// deleting one of those would serve that site with Traefik's self-signed
// default until a new certificate arrives, and spend a Let's Encrypt issuance
// getting back to where it started.
func (s *Server) handleCertDelete(w http.ResponseWriter, r *http.Request) {
	domain := r.PathValue("domain")
	var target *CertView
	for _, v := range s.certViews() {
		if strings.EqualFold(v.Domain, domain) {
			target = &v
			break
		}
	}
	if target == nil {
		redirectSystem(w, r, "No certificate for "+domain+" in the store.")
		return
	}
	if target.UsedBy != "" {
		redirectSystem(w, r, "The certificate for "+target.Domain+" is still routed by "+target.UsedBy+" — delete the application first.")
		return
	}
	path, writable := s.acmePath()
	if !writable {
		redirectSystem(w, r, "Traefik's certificate store is mounted read-only. Update docker-compose.yml to bind-mount "+s.cfg.TraefikDir+" into the dashboard, then restart the stack.")
		return
	}
	if err := certs.Delete(path, target.Domain); err != nil {
		redirectSystem(w, r, "Certificate deletion failed: "+err.Error())
		return
	}

	// The file is only half of it: Traefik holds the certificates in memory and
	// would write this one back on its next save.
	s.audit(r, "cert.delete", target.Domain, "")
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 60*time.Second)
	defer cancel()
	if err := s.dock.RestartTraefik(ctx); err != nil {
		redirectSystem(w, r, "Certificate for "+target.Domain+" removed, but Traefik could not be restarted ("+err.Error()+") — restart it by hand or it will write the certificate back.")
		return
	}
	redirectSystem(w, r, "Certificate for "+target.Domain+" deleted and Traefik restarted.")
}

func redirectSystem(w http.ResponseWriter, r *http.Request, msg string) {
	http.Redirect(w, r, "/system?msg="+url.QueryEscape(msg), http.StatusSeeOther)
}

func (s *Server) handleSystem(w http.ResponseWriter, r *http.Request) {
	data := s.systemData(r)
	if msg := r.URL.Query().Get("msg"); msg != "" {
		data["Saved"] = msg
	}
	s.render(w, r, "system", data)
}

// handleCleanup removes everything Docker is holding that no application, no
// rollback and no part of the platform still needs.
func (s *Server) handleCleanup(w http.ResponseWriter, r *http.Request) {
	ids, err := s.appIDs()
	if err != nil {
		redirectSystem(w, r, "Cleanup cancelled: the application list could not be read ("+err.Error()+"), and without it nothing can be told apart from a leftover.")
		return
	}
	withVolumes := r.FormValue("volumes") == "on"

	// Deleting gigabytes of layers takes longer than a browser is willing to
	// wait for a response, and a sweep abandoned half-way is worse than one
	// never started: the images are gone but the containers holding them are
	// not, so a second attempt finds a different daemon than the scan did.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 10*time.Minute)
	defer cancel()

	rep, err := s.dock.Cleanup(ctx, ids, withVolumes)
	if err != nil {
		redirectSystem(w, r, "Cleanup failed: "+err.Error())
		return
	}
	detail := ""
	if withVolumes {
		detail = "including orphaned volumes"
	}
	s.audit(r, "system.cleanup", docker.HumanSize(rep.Bytes), detail)
	redirectSystem(w, r, rep.Summary())
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

// offsiteView is the object-storage configuration as the form shows it. The
// secret key is reported only as present or absent, never echoed back.
func offsiteView(database *sql.DB) map[string]any {
	return map[string]any{
		"Endpoint":  db.GetSetting(database, db.SettingOffsiteEndpoint),
		"Region":    db.GetSetting(database, db.SettingOffsiteRegion),
		"Bucket":    db.GetSetting(database, db.SettingOffsiteBucket),
		"Prefix":    db.GetSetting(database, db.SettingOffsitePrefix),
		"AccessKey": db.GetSetting(database, db.SettingOffsiteAccessKey),
		"SecretSet": db.GetSetting(database, db.SettingOffsiteSecretKey) != "",
	}
}

// handleOffsiteSettings stores the object-storage destination for backups.
func (s *Server) handleOffsiteSettings(w http.ResponseWriter, r *http.Request) {
	for field, key := range map[string]string{
		"offsite_endpoint":   db.SettingOffsiteEndpoint,
		"offsite_region":     db.SettingOffsiteRegion,
		"offsite_bucket":     db.SettingOffsiteBucket,
		"offsite_prefix":     db.SettingOffsitePrefix,
		"offsite_access_key": db.SettingOffsiteAccessKey,
	} {
		db.SetSetting(s.db, key, strings.TrimSpace(r.FormValue(field)))
	}

	// Encrypted at rest like an app's env content: this key can read and delete
	// every archive in the bucket.
	if raw := strings.TrimSpace(r.FormValue("offsite_secret_key")); raw != "" {
		enc, err := s.keyring.Encrypt(raw)
		if err != nil {
			http.Error(w, "could not store the secret key: "+err.Error(), http.StatusInternalServerError)
			return
		}
		db.SetSetting(s.db, db.SettingOffsiteSecretKey, enc)
	}
	if r.FormValue("clear_offsite_secret_key") == "on" {
		db.SetSetting(s.db, db.SettingOffsiteSecretKey, "")
	}

	s.audit(r, "settings.offsite", db.GetSetting(s.db, db.SettingOffsiteBucket), "")
	http.Redirect(w, r, "/system?msg="+url.QueryEscape("Offsite settings saved."), http.StatusSeeOther)
}

// handleOffsiteTest uploads a small probe object, which is the only way to know
// the endpoint, credentials and bucket policy actually work together. A wrong
// signature or a missing PutObject permission is otherwise discovered on the
// night the VPS dies.
func (s *Server) handleOffsiteTest(w http.ResponseWriter, r *http.Request) {
	cfg, err := offsite.Load(s.db, s.keyring)
	if err != nil {
		http.Redirect(w, r, "/system?msg="+url.QueryEscape("Offsite test failed: "+err.Error()), http.StatusSeeOther)
		return
	}
	s.audit(r, "offsite.test", cfg.Bucket, "")

	msg := "Offsite test upload succeeded — the credentials and bucket work."
	if err := offsite.UploadProbe(cfg); err != nil {
		msg = "Offsite test failed: " + err.Error()
	}
	http.Redirect(w, r, "/system?msg="+url.QueryEscape(msg), http.StatusSeeOther)
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
