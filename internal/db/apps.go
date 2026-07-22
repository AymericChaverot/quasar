package db

import (
	"database/sql"
	"strings"
	"time"
)

type App struct {
	ID            string
	Name          string
	Subdomain     string
	DeployType    string // "image", "git" or "compose"
	ImageRef      string
	GitURL        string
	GitBranch     string
	ComposeYAML   string
	Port          int
	EnvContent    string
	DataMount     string // container path bound to apps/<id>/data, empty = no volume
	WebhookSecret string
	CPULimit      float64 // CPUs, 0 = unlimited
	MemLimitMB    int64   // MB, 0 = unlimited
	CustomDomains string  // comma-separated extra domains routed to this app
	HealthPath    string  // HTTP path probed for health, empty = disabled
	BasicAuthUser string  // Traefik basic auth username, empty = no protection
	BasicAuthHash string  // bcrypt hash in htpasswd format
	SortOrder     int     // manual position in the dashboard list
	CreatedAt     time.Time
}

// CustomDomainList splits CustomDomains for templates and Traefik rules.
func (a *App) CustomDomainList() []string {
	if a.CustomDomains == "" {
		return nil
	}
	var out []string
	for _, d := range strings.Split(a.CustomDomains, ",") {
		if d = strings.TrimSpace(d); d != "" {
			out = append(out, d)
		}
	}
	return out
}

const appCols = "id, name, subdomain, deploy_type, image_ref, git_url, git_branch, compose_yaml, port, env_content, data_mount, webhook_secret, cpu_limit, mem_limit_mb, custom_domains, health_path, basic_auth_user, basic_auth_hash, sort_order, created_at"

func scanApp(row interface{ Scan(...any) error }) (*App, error) {
	var a App
	err := row.Scan(&a.ID, &a.Name, &a.Subdomain, &a.DeployType, &a.ImageRef, &a.GitURL,
		&a.GitBranch, &a.ComposeYAML, &a.Port, &a.EnvContent, &a.DataMount,
		&a.WebhookSecret, &a.CPULimit, &a.MemLimitMB, &a.CustomDomains,
		&a.HealthPath, &a.BasicAuthUser, &a.BasicAuthHash, &a.SortOrder, &a.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

func InsertApp(db *sql.DB, a *App) error {
	// New apps go to the bottom of the manually ordered list.
	_, err := db.Exec(`INSERT INTO apps (id, name, subdomain, deploy_type, image_ref, git_url, git_branch, compose_yaml, port, env_content, data_mount, webhook_secret, cpu_limit, mem_limit_mb, custom_domains, health_path, basic_auth_user, basic_auth_hash, sort_order)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, (SELECT COALESCE(MAX(sort_order), 0) + 1 FROM apps))`,
		a.ID, a.Name, a.Subdomain, a.DeployType, a.ImageRef, a.GitURL, a.GitBranch, a.ComposeYAML,
		a.Port, a.EnvContent, a.DataMount, a.WebhookSecret, a.CPULimit, a.MemLimitMB, a.CustomDomains,
		a.HealthPath, a.BasicAuthUser, a.BasicAuthHash)
	return err
}

// SetAppOrder writes an app's explicit position in the list.
func SetAppOrder(db *sql.DB, id string, order int) {
	db.Exec("UPDATE apps SET sort_order = ? WHERE id = ?", order, id)
}

func UpdateAppHealth(db *sql.DB, id, healthPath string) error {
	_, err := db.Exec("UPDATE apps SET health_path = ? WHERE id = ?", healthPath, id)
	return err
}

func UpdateAppBasicAuth(db *sql.DB, id, user, hash string) error {
	_, err := db.Exec("UPDATE apps SET basic_auth_user = ?, basic_auth_hash = ? WHERE id = ?", user, hash, id)
	return err
}

func UpdateAppDomains(db *sql.DB, id, customDomains string) error {
	_, err := db.Exec("UPDATE apps SET custom_domains = ? WHERE id = ?", customDomains, id)
	return err
}

func UpdateAppEnv(db *sql.DB, id, envContent string) error {
	_, err := db.Exec("UPDATE apps SET env_content = ? WHERE id = ?", envContent, id)
	return err
}

func UpdateAppCompose(db *sql.DB, id, composeYAML string) error {
	_, err := db.Exec("UPDATE apps SET compose_yaml = ? WHERE id = ?", composeYAML, id)
	return err
}

func DeleteApp(db *sql.DB, id string) error {
	_, err := db.Exec("DELETE FROM apps WHERE id = ?", id)
	return err
}

func GetApp(db *sql.DB, id string) (*App, error) {
	return scanApp(db.QueryRow("SELECT "+appCols+" FROM apps WHERE id = ?", id))
}

func ListApps(db *sql.DB) ([]*App, error) {
	rows, err := db.Query("SELECT " + appCols + " FROM apps ORDER BY sort_order, created_at")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var apps []*App
	for rows.Next() {
		a, err := scanApp(rows)
		if err != nil {
			return nil, err
		}
		apps = append(apps, a)
	}
	return apps, rows.Err()
}

func SubdomainTaken(db *sql.DB, subdomain string) (bool, error) {
	var n int
	err := db.QueryRow("SELECT COUNT(*) FROM apps WHERE subdomain = ?", subdomain).Scan(&n)
	return n > 0, err
}
