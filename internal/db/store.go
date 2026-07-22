package db

import (
	"database/sql"
	"time"
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
}

// Duration returns the deploy duration for finished deployments.
func (d *Deployment) Duration() string {
	if !d.FinishedAt.Valid {
		return "…"
	}
	return d.FinishedAt.Time.Sub(d.StartedAt).Round(time.Second).String()
}

func StartDeployment(db *sql.DB, appID, source string) int64 {
	res, err := db.Exec("INSERT INTO deployments (app_id, source) VALUES (?, ?)", appID, source)
	if err != nil {
		return 0
	}
	id, _ := res.LastInsertId()
	return id
}

func FinishDeployment(db *sql.DB, id int64, status, detail, imageTag string) {
	db.Exec("UPDATE deployments SET status = ?, detail = ?, image_tag = ?, finished_at = ? WHERE id = ?",
		status, detail, imageTag, time.Now(), id)
}

func ListDeployments(db *sql.DB, appID string, limit int) ([]*Deployment, error) {
	rows, err := db.Query(`SELECT id, app_id, source, image_tag, status, detail, started_at, finished_at
		FROM deployments WHERE app_id = ? ORDER BY id DESC LIMIT ?`, appID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Deployment
	for rows.Next() {
		var d Deployment
		if err := rows.Scan(&d.ID, &d.AppID, &d.Source, &d.ImageTag, &d.Status, &d.Detail, &d.StartedAt, &d.FinishedAt); err != nil {
			return nil, err
		}
		out = append(out, &d)
	}
	return out, rows.Err()
}

func DeleteDeployments(db *sql.DB, appID string) {
	db.Exec("DELETE FROM deployments WHERE app_id = ?", appID)
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
	SettingGitToken        = "git_token"        // token injected into https git clones
	SettingNotifyURL       = "notify_url"       // Discord/Slack-compatible webhook
	SettingBackupRetention = "backup_retention" // how many backup archives to keep
	SettingBackupAuto      = "backup_auto"      // "true" to run a daily backup
)

func GetSetting(db *sql.DB, key string) string {
	var v string
	db.QueryRow("SELECT value FROM settings WHERE key = ?", key).Scan(&v)
	return v
}

func SetSetting(db *sql.DB, key, value string) error {
	_, err := db.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES (?, ?)", key, value)
	return err
}
