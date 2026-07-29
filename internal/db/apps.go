package db

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"quasar/internal/secrets"
)

// How a git app's checkout is built. A repository that carries a compose file
// describes a whole stack, and a Dockerfile can only ever describe one service
// of it, so the compose file wins unless the operator says otherwise — hence
// GitBuildAuto being the default and preferring compose.
const (
	GitBuildAuto       = ""
	GitBuildDockerfile = "dockerfile"
	GitBuildCompose    = "compose"
)

type App struct {
	ID          string
	Name        string
	Subdomain   string
	DeployType  string // "image", "git" or "compose"
	ImageRef    string
	GitURL      string
	GitBranch   string
	GitBuild    string // git apps: GitBuildAuto/Dockerfile/Compose
	ComposeYAML string
	// ComposeService is the stack's service the domain is routed to, empty for
	// the one Quasar works out from the compose file itself. It is only needed
	// when several services could plausibly serve the site.
	ComposeService  string
	Port            int
	EnvContent      string
	DataMount       string // container path bound to apps/<id>/data, empty = no volume
	WebhookSecret   string
	CPULimit        float64 // CPUs, 0 = unlimited
	MemLimitMB      int64   // MB, 0 = unlimited
	CustomDomains   string  // comma-separated extra domains routed to this app
	HealthPath      string  // HTTP path probed for health, empty = disabled
	PreBackupCmd    string  // dumped into the backup archive before archiving, empty = none
	RateLimit       int     // requests per second per client IP, 0 = unlimited
	IPAllowCIDRs    string  // comma-separated CIDRs allowed to reach the app, empty = all
	SecurityHeaders bool    // HSTS and the usual browser hardening headers
	BasicAuthUser   string  // Traefik basic auth username, empty = no protection
	BasicAuthHash   string  // bcrypt hash in htpasswd format
	SortOrder       int     // manual position in the dashboard list
	CreatedAt       time.Time
}

// IPAllowList splits IPAllowCIDRs into entries for the Traefik middleware.
func (a *App) IPAllowList() []string {
	var out []string
	for _, c := range strings.Split(a.IPAllowCIDRs, ",") {
		if c = strings.TrimSpace(c); c != "" {
			out = append(out, c)
		}
	}
	return out
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

const appCols = "id, name, subdomain, deploy_type, image_ref, git_url, git_branch, git_build, compose_yaml, compose_service, port, env_content, data_mount, webhook_secret, cpu_limit, mem_limit_mb, custom_domains, health_path, basic_auth_user, basic_auth_hash, sort_order, pre_backup_cmd, rate_limit, ip_allow_cidrs, security_headers, created_at"

// scanApp reads one row and decrypts its at-rest-encrypted columns, so every
// *App leaving the db package carries plaintext EnvContent/ComposeYAML —
// callers never need to know encryption is involved.
func scanApp(row interface{ Scan(...any) error }, k *secrets.Keyring) (*App, error) {
	var a App
	err := row.Scan(&a.ID, &a.Name, &a.Subdomain, &a.DeployType, &a.ImageRef, &a.GitURL,
		&a.GitBranch, &a.GitBuild, &a.ComposeYAML, &a.ComposeService, &a.Port, &a.EnvContent, &a.DataMount,
		&a.WebhookSecret, &a.CPULimit, &a.MemLimitMB, &a.CustomDomains,
		&a.HealthPath, &a.BasicAuthUser, &a.BasicAuthHash, &a.SortOrder,
		&a.PreBackupCmd, &a.RateLimit, &a.IPAllowCIDRs, &a.SecurityHeaders, &a.CreatedAt)
	if err != nil {
		return nil, err
	}
	if a.EnvContent, err = k.Decrypt(a.EnvContent); err != nil {
		return nil, fmt.Errorf("app %s: decrypt env: %w", a.ID, err)
	}
	if a.ComposeYAML, err = k.Decrypt(a.ComposeYAML); err != nil {
		return nil, fmt.Errorf("app %s: decrypt compose yaml: %w", a.ID, err)
	}
	// Encrypted like the others: a dump command often carries the credentials
	// it authenticates with.
	if a.PreBackupCmd, err = k.Decrypt(a.PreBackupCmd); err != nil {
		return nil, fmt.Errorf("app %s: decrypt pre-backup command: %w", a.ID, err)
	}
	return &a, nil
}

func InsertApp(db *sql.DB, k *secrets.Keyring, a *App) error {
	envContent, err := k.Encrypt(a.EnvContent)
	if err != nil {
		return fmt.Errorf("encrypt env: %w", err)
	}
	composeYAML, err := k.Encrypt(a.ComposeYAML)
	if err != nil {
		return fmt.Errorf("encrypt compose yaml: %w", err)
	}
	preBackup, err := k.Encrypt(a.PreBackupCmd)
	if err != nil {
		return fmt.Errorf("encrypt pre-backup command: %w", err)
	}
	// New apps go to the bottom of the manually ordered list.
	_, err = db.Exec(`INSERT INTO apps (id, name, subdomain, deploy_type, image_ref, git_url, git_branch, git_build, compose_yaml, port, env_content, data_mount, webhook_secret, cpu_limit, mem_limit_mb, custom_domains, health_path, basic_auth_user, basic_auth_hash, pre_backup_cmd, rate_limit, ip_allow_cidrs, security_headers, sort_order)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, (SELECT COALESCE(MAX(sort_order), 0) + 1 FROM apps))`,
		a.ID, a.Name, a.Subdomain, a.DeployType, a.ImageRef, a.GitURL, a.GitBranch, a.GitBuild, composeYAML,
		a.Port, envContent, a.DataMount, a.WebhookSecret, a.CPULimit, a.MemLimitMB, a.CustomDomains,
		a.HealthPath, a.BasicAuthUser, a.BasicAuthHash, preBackup,
		a.RateLimit, a.IPAllowCIDRs, a.SecurityHeaders)
	return err
}

// UpdateAppProtection stores the edge protections Traefik applies in front of
// the app. They take effect on the next deploy, since Docker labels are fixed
// when a container is created.
func UpdateAppProtection(db *sql.DB, id string, rateLimit int, ipAllowCIDRs string, securityHeaders bool) error {
	_, err := db.Exec(
		"UPDATE apps SET rate_limit = ?, ip_allow_cidrs = ?, security_headers = ? WHERE id = ?",
		rateLimit, ipAllowCIDRs, securityHeaders, id)
	return err
}

// UpdateAppPreBackup stores the command run inside the container before a
// backup, whose stdout is archived as the app's dump.
func UpdateAppPreBackup(db *sql.DB, k *secrets.Keyring, id, command string) error {
	enc, err := k.Encrypt(command)
	if err != nil {
		return fmt.Errorf("encrypt pre-backup command: %w", err)
	}
	_, err = db.Exec("UPDATE apps SET pre_backup_cmd = ? WHERE id = ?", enc, id)
	return err
}

// SetAppOrder writes an app's explicit position in the list.
func SetAppOrder(db *sql.DB, id string, order int) {
	db.Exec("UPDATE apps SET sort_order = ? WHERE id = ?", order, id)
}

// UpdateAppGitBuild stores how a git app's checkout is built. It takes effect
// on the next deploy, which is the only moment the choice is acted on.
func UpdateAppGitBuild(db *sql.DB, id, mode string) error {
	switch mode {
	case GitBuildAuto, GitBuildDockerfile, GitBuildCompose:
	default:
		return fmt.Errorf("unknown git build mode %q", mode)
	}
	_, err := db.Exec("UPDATE apps SET git_build = ? WHERE id = ?", mode, id)
	return err
}

// UpdateAppComposeService stores which service of a stack the domain is routed
// to, empty to go back to the one Quasar works out for itself. It takes effect
// on the next deploy, which is when the compose file is rewritten.
func UpdateAppComposeService(db *sql.DB, id, service string) error {
	_, err := db.Exec("UPDATE apps SET compose_service = ? WHERE id = ?", service, id)
	return err
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

func UpdateAppEnv(db *sql.DB, k *secrets.Keyring, id, envContent string) error {
	enc, err := k.Encrypt(envContent)
	if err != nil {
		return fmt.Errorf("encrypt env: %w", err)
	}
	_, err = db.Exec("UPDATE apps SET env_content = ? WHERE id = ?", enc, id)
	return err
}

func UpdateAppCompose(db *sql.DB, k *secrets.Keyring, id, composeYAML string) error {
	enc, err := k.Encrypt(composeYAML)
	if err != nil {
		return fmt.Errorf("encrypt compose yaml: %w", err)
	}
	_, err = db.Exec("UPDATE apps SET compose_yaml = ? WHERE id = ?", enc, id)
	return err
}

func DeleteApp(db *sql.DB, id string) error {
	_, err := db.Exec("DELETE FROM apps WHERE id = ?", id)
	return err
}

func GetApp(db *sql.DB, k *secrets.Keyring, id string) (*App, error) {
	return scanApp(db.QueryRow("SELECT "+appCols+" FROM apps WHERE id = ?", id), k)
}

func ListApps(db *sql.DB, k *secrets.Keyring) ([]*App, error) {
	rows, err := db.Query("SELECT " + appCols + " FROM apps ORDER BY sort_order, created_at")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var apps []*App
	for rows.Next() {
		a, err := scanApp(rows, k)
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

// ResealApps re-seals every app's env and compose data from one key to
// another, returning how many rows changed.
//
// It exists for restores: an archive taken on another host has its rows sealed
// with that host's master key, so on a rebuilt VPS the live key cannot open
// them. Rather than swapping this install's key — which would orphan anything
// already written with it and need a restart — the restored rows are moved onto
// the live key in place.
func ResealApps(database *sql.DB, from, to *secrets.Keyring) (int, error) {
	rows, err := database.Query("SELECT id, env_content, compose_yaml, pre_backup_cmd FROM apps")
	if err != nil {
		return 0, err
	}
	type row struct{ id, env, compose, preBackup string }
	var all []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.env, &r.compose, &r.preBackup); err != nil {
			rows.Close()
			return 0, err
		}
		all = append(all, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	resealed := 0
	for _, r := range all {
		// Values with no encryption marker pass through Decrypt untouched, so
		// plaintext rows from an old archive are simply encrypted on the way in.
		env, err := from.Decrypt(r.env)
		if err != nil {
			return resealed, fmt.Errorf("app %s: open env with the supplied key: %w", r.id, err)
		}
		compose, err := from.Decrypt(r.compose)
		if err != nil {
			return resealed, fmt.Errorf("app %s: open compose yaml with the supplied key: %w", r.id, err)
		}
		preBackup, err := from.Decrypt(r.preBackup)
		if err != nil {
			return resealed, fmt.Errorf("app %s: open pre-backup command with the supplied key: %w", r.id, err)
		}
		if env, err = to.Encrypt(env); err != nil {
			return resealed, fmt.Errorf("app %s: reseal env: %w", r.id, err)
		}
		if compose, err = to.Encrypt(compose); err != nil {
			return resealed, fmt.Errorf("app %s: reseal compose yaml: %w", r.id, err)
		}
		if preBackup, err = to.Encrypt(preBackup); err != nil {
			return resealed, fmt.Errorf("app %s: reseal pre-backup command: %w", r.id, err)
		}
		if _, err := database.Exec(
			"UPDATE apps SET env_content = ?, compose_yaml = ?, pre_backup_cmd = ? WHERE id = ?",
			env, compose, preBackup, r.id); err != nil {
			return resealed, fmt.Errorf("app %s: %w", r.id, err)
		}
		resealed++
	}
	return resealed, nil
}

// EncryptLegacyApps re-encrypts any apps.env_content/compose_yaml columns
// still holding plaintext from before at-rest encryption existed, and
// returns how many rows it touched. Meant to run once at startup so
// upgrading an existing install closes the plaintext gap immediately,
// instead of only as each app happens to be edited next.
func EncryptLegacyApps(database *sql.DB, k *secrets.Keyring) (int, error) {
	rows, err := database.Query("SELECT id, env_content, compose_yaml FROM apps")
	if err != nil {
		return 0, err
	}
	type legacyRow struct{ id, env, compose string }
	var legacy []legacyRow
	for rows.Next() {
		var r legacyRow
		if err := rows.Scan(&r.id, &r.env, &r.compose); err != nil {
			rows.Close()
			return 0, err
		}
		if (r.env != "" && !secrets.IsEncrypted(r.env)) || (r.compose != "" && !secrets.IsEncrypted(r.compose)) {
			legacy = append(legacy, r)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	for _, r := range legacy {
		env := r.env
		if env != "" && !secrets.IsEncrypted(env) {
			if env, err = k.Encrypt(env); err != nil {
				return 0, fmt.Errorf("app %s: encrypt env: %w", r.id, err)
			}
		}
		compose := r.compose
		if compose != "" && !secrets.IsEncrypted(compose) {
			if compose, err = k.Encrypt(compose); err != nil {
				return 0, fmt.Errorf("app %s: encrypt compose yaml: %w", r.id, err)
			}
		}
		if _, err := database.Exec("UPDATE apps SET env_content = ?, compose_yaml = ? WHERE id = ?", env, compose, r.id); err != nil {
			return 0, fmt.Errorf("app %s: %w", r.id, err)
		}
	}
	return len(legacy), nil
}
