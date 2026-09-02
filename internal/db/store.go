package db

import (
	"database/sql"
	"fmt"
	"time"

	"quasar/internal/secrets"
)

// --- Deployments (history + rollback) ---------------------------------------

type Deployment struct {
	ID         int64
	AppID      string
	Source     string // "manual", "webhook", "rollback"
	ImageTag   string
	Status     string // "running", "success", "failed"
	Detail     string
	StartedAt  time.Time
	FinishedAt sql.NullTime

	// HasCompose is whether this deployment kept a copy of the compose file it
	// ran, which is what a stack goes back to instead of an image tag. Only
	// whether, not the file itself: the list draws a table and a page of rows
	// decrypted to answer a yes-or-no question is a page of secrets in memory
	// for nothing. DeploymentCompose reads the one that is actually wanted.
	HasCompose bool
}

// Duration returns the deploy duration for finished deployments.
func (d *Deployment) Duration() string {
	if !d.FinishedAt.Valid {
		return "…"
	}
	return d.FinishedAt.Time.Sub(d.StartedAt).Round(time.Second).String()
}

// StartDeployment opens a deployment record and returns its id.
//
// compose is the file this deployment is about to run, for a stack whose file
// Quasar owns; empty for everything else. It is written at the start rather
// than at the end because the start is when it is known — by the end the
// application row may already have been edited again, and the history would
// record what is current rather than what ran.
func StartDeployment(db *sql.DB, k *secrets.Keyring, appID, source, compose string) int64 {
	var enc string
	if compose != "" {
		var err error
		if enc, err = k.Encrypt(compose); err != nil {
			// The deployment is still worth recording without it; what is lost
			// is the ability to come back to this one, not the history.
			enc = ""
		}
	}
	res, err := db.Exec("INSERT INTO deployments (app_id, source, compose_yaml) VALUES (?, ?, ?)", appID, source, enc)
	if err != nil {
		return 0
	}
	id, _ := res.LastInsertId()
	return id
}

// DeploymentCompose is the compose file one deployment ran, for the rollback
// that is about to put it back. It refuses a deployment belonging to another
// application: the id travels in a URL, and an id from one application's
// history must not fetch another's file.
func DeploymentCompose(db *sql.DB, k *secrets.Keyring, appID string, id int64) (string, error) {
	var enc string
	if err := db.QueryRow("SELECT compose_yaml FROM deployments WHERE id = ? AND app_id = ?", id, appID).Scan(&enc); err != nil {
		return "", err
	}
	if enc == "" {
		return "", fmt.Errorf("deployment %d kept no compose file", id)
	}
	return k.Decrypt(enc)
}

func FinishDeployment(db *sql.DB, id int64, status, detail, imageTag string) error {
	_, err := db.Exec("UPDATE deployments SET status = ?, detail = ?, image_tag = ?, finished_at = ? WHERE id = ?",
		status, detail, imageTag, time.Now(), id)
	return err
}

func ListDeployments(db *sql.DB, appID string, limit int) ([]*Deployment, error) {
	rows, err := db.Query(`SELECT id, app_id, source, image_tag, status, detail, started_at, finished_at,
		compose_yaml != '' FROM deployments WHERE app_id = ? ORDER BY id DESC LIMIT ?`, appID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Deployment
	for rows.Next() {
		var d Deployment
		if err := rows.Scan(&d.ID, &d.AppID, &d.Source, &d.ImageTag, &d.Status, &d.Detail,
			&d.StartedAt, &d.FinishedAt, &d.HasCompose); err != nil {
			return nil, err
		}
		out = append(out, &d)
	}
	return out, rows.Err()
}

func DeleteDeployments(db *sql.DB, appID string) error {
	_, err := db.Exec("DELETE FROM deployments WHERE app_id = ?", appID)
	return err
}

// --- Registries (private image pulls) ---------------------------------------

type Registry struct {
	ID       int64
	Server   string // e.g. "docker.io", "ghcr.io", "registry.example.com"
	Username string
	Secret   string
}

func InsertRegistry(db *sql.DB, r *Registry) error {
	_, err := db.Exec("INSERT OR REPLACE INTO registries (server, username, secret) VALUES (?, ?, ?)",
		r.Server, r.Username, r.Secret)
	return err
}

func DeleteRegistry(db *sql.DB, id int64) error {
	_, err := db.Exec("DELETE FROM registries WHERE id = ?", id)
	return err
}

func ListRegistries(db *sql.DB) ([]*Registry, error) {
	rows, err := db.Query("SELECT id, server, username, secret FROM registries ORDER BY server")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Registry
	for rows.Next() {
		var r Registry
		if err := rows.Scan(&r.ID, &r.Server, &r.Username, &r.Secret); err != nil {
			return nil, err
		}
		out = append(out, &r)
	}
	return out, rows.Err()
}

// RegistryFor returns the credentials matching an image reference's registry
// host, or nil when the registry is unknown.
func RegistryFor(db *sql.DB, server string) *Registry {
	var r Registry
	err := db.QueryRow("SELECT id, server, username, secret FROM registries WHERE server = ?", server).
		Scan(&r.ID, &r.Server, &r.Username, &r.Secret)
	if err != nil {
		return nil
	}
	return &r
}

// --- Settings (key-value platform configuration) -----------------------------

// Well-known settings keys.
const (
	// Deprecated: the single platform-wide git token, superseded by the
	// git_credentials table. Kept only so MigrateGitToken can find and clear
	// what an install created before per-host credentials existed.
	SettingGitToken = "git_token"

	SettingNotifyURL       = "notify_url"       // Discord/Slack-compatible webhook
	SettingBackupRetention = "backup_retention" // how many backup archives to keep
	SettingBackupAuto      = "backup_auto"      // "true" to run a daily backup

	// Host usage percentages that trigger a notification; 0 turns one off.
	SettingAlertDisk = "alert_disk_percent"
	SettingAlertMem  = "alert_mem_percent"
	SettingAlertCPU  = "alert_cpu_percent"

	// Additional notification channels. Each is independent of the others, so
	// one broken destination cannot silence the platform.
	SettingNtfyURL      = "ntfy_url" // full topic URL, e.g. https://ntfy.sh/my-topic
	SettingSMTPHost     = "smtp_host"
	SettingSMTPPort     = "smtp_port"
	SettingSMTPUser     = "smtp_user"
	SettingSMTPPassword = "smtp_password"
	SettingSMTPFrom     = "smtp_from"
	SettingSMTPTo       = "smtp_to"

	// S3-compatible destination for backup archives. The secret key is stored
	// encrypted with the master key, like an app's env content.
	SettingOffsiteEndpoint  = "offsite_endpoint"
	SettingOffsiteRegion    = "offsite_region"
	SettingOffsiteBucket    = "offsite_bucket"
	SettingOffsitePrefix    = "offsite_prefix"
	SettingOffsiteAccessKey = "offsite_access_key"
	SettingOffsiteSecretKey = "offsite_secret_key"
)

func GetSetting(db *sql.DB, key string) string {
	var v string
	// A key that was never stored and a query that will not run are the same
	// answer here: the setting has no value.
	if err := db.QueryRow("SELECT value FROM settings WHERE key = ?", key).Scan(&v); err != nil {
		return ""
	}
	return v
}

func SetSetting(db *sql.DB, key, value string) error {
	_, err := db.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES (?, ?)", key, value)
	return err
}
