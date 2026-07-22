package db

import (
	"database/sql"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS users (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	username      TEXT NOT NULL UNIQUE,
	password_hash TEXT NOT NULL,
	totp_secret   TEXT NOT NULL DEFAULT '',
	totp_enabled  INTEGER NOT NULL DEFAULT 0,
	created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS sessions (
	token       TEXT PRIMARY KEY,
	user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	pending_2fa INTEGER NOT NULL DEFAULT 0,
	expires_at  DATETIME NOT NULL
);

CREATE TABLE IF NOT EXISTS apps (
	id             TEXT PRIMARY KEY,
	name           TEXT NOT NULL,
	subdomain      TEXT NOT NULL UNIQUE,
	deploy_type    TEXT NOT NULL CHECK (deploy_type IN ('image', 'git', 'compose')),
	image_ref      TEXT NOT NULL DEFAULT '',
	git_url        TEXT NOT NULL DEFAULT '',
	git_branch     TEXT NOT NULL DEFAULT 'main',
	compose_yaml   TEXT NOT NULL DEFAULT '',
	port           INTEGER NOT NULL DEFAULT 80,
	env_content    TEXT NOT NULL DEFAULT '',
	data_mount     TEXT NOT NULL DEFAULT '',
	webhook_secret TEXT NOT NULL DEFAULT '',
	cpu_limit      REAL NOT NULL DEFAULT 0,
	mem_limit_mb   INTEGER NOT NULL DEFAULT 0,
	custom_domains TEXT NOT NULL DEFAULT '',
	health_path    TEXT NOT NULL DEFAULT '',
	basic_auth_user TEXT NOT NULL DEFAULT '',
	basic_auth_hash TEXT NOT NULL DEFAULT '',
	created_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS tasks (
	id               INTEGER PRIMARY KEY AUTOINCREMENT,
	app_id           TEXT NOT NULL,
	command          TEXT NOT NULL,
	interval_minutes INTEGER NOT NULL DEFAULT 0,
	last_run         DATETIME,
	last_status      TEXT NOT NULL DEFAULT '',
	last_output      TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS health_history (
	app_id TEXT NOT NULL,
	ts     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	ok     INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_health_app_ts ON health_history(app_id, ts);

CREATE TABLE IF NOT EXISTS metrics (
	ts   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	cpu  REAL NOT NULL,
	mem  REAL NOT NULL,
	disk REAL NOT NULL
);

CREATE TABLE IF NOT EXISTS app_metrics (
	app_id TEXT NOT NULL,
	ts     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	cpu    REAL NOT NULL,
	mem_mb REAL NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_app_metrics ON app_metrics(app_id, ts);

CREATE TABLE IF NOT EXISTS deployments (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	app_id      TEXT NOT NULL,
	source      TEXT NOT NULL DEFAULT 'manual',
	image_tag   TEXT NOT NULL DEFAULT '',
	status      TEXT NOT NULL DEFAULT 'running',
	detail      TEXT NOT NULL DEFAULT '',
	started_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	finished_at DATETIME
);

CREATE TABLE IF NOT EXISTS registries (
	id       INTEGER PRIMARY KEY AUTOINCREMENT,
	server   TEXT NOT NULL UNIQUE,
	username TEXT NOT NULL,
	secret   TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS settings (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
`

// migrations are applied best-effort on top of the base schema so existing
// databases gain new columns; "duplicate column" errors are expected.
var migrations = []string{
	"ALTER TABLE apps ADD COLUMN webhook_secret TEXT NOT NULL DEFAULT ''",
	"ALTER TABLE apps ADD COLUMN cpu_limit REAL NOT NULL DEFAULT 0",
	"ALTER TABLE apps ADD COLUMN mem_limit_mb INTEGER NOT NULL DEFAULT 0",
	"ALTER TABLE apps ADD COLUMN custom_domains TEXT NOT NULL DEFAULT ''",
	"ALTER TABLE apps ADD COLUMN health_path TEXT NOT NULL DEFAULT ''",
	"ALTER TABLE apps ADD COLUMN basic_auth_user TEXT NOT NULL DEFAULT ''",
	"ALTER TABLE apps ADD COLUMN basic_auth_hash TEXT NOT NULL DEFAULT ''",
	"ALTER TABLE users ADD COLUMN totp_secret TEXT NOT NULL DEFAULT ''",
	"ALTER TABLE users ADD COLUMN totp_enabled INTEGER NOT NULL DEFAULT 0",
	"ALTER TABLE sessions ADD COLUMN pending_2fa INTEGER NOT NULL DEFAULT 0",
}

func Open(path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	// SQLite handles one writer at a time; a single connection avoids SQLITE_BUSY.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	for _, m := range migrations {
		db.Exec(m) // duplicate-column errors are fine on fresh databases
	}
	return db, nil
}
