package db

import (
	"database/sql"
	"log"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS users (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	username      TEXT NOT NULL UNIQUE,
	password_hash TEXT NOT NULL,
	totp_secret   TEXT NOT NULL DEFAULT '',
	totp_enabled  INTEGER NOT NULL DEFAULT 0,
	role          TEXT NOT NULL DEFAULT 'admin',
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
	git_build      TEXT NOT NULL DEFAULT '',
	compose_yaml   TEXT NOT NULL DEFAULT '',
	compose_service TEXT NOT NULL DEFAULT '',
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
	sort_order     INTEGER NOT NULL DEFAULT 0,
	pre_backup_cmd TEXT NOT NULL DEFAULT '',
	rate_limit     INTEGER NOT NULL DEFAULT 0,
	ip_allow_cidrs TEXT NOT NULL DEFAULT '',
	security_headers INTEGER NOT NULL DEFAULT 0,
	station_id     TEXT NOT NULL DEFAULT '',
	station_params TEXT NOT NULL DEFAULT '',
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

CREATE TABLE IF NOT EXISTS app_logs (
	id     INTEGER PRIMARY KEY AUTOINCREMENT,
	app_id TEXT NOT NULL,
	ts     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	line   TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_app_logs_app_ts ON app_logs(app_id, ts);

CREATE TABLE IF NOT EXISTS registries (
	id       INTEGER PRIMARY KEY AUTOINCREMENT,
	server   TEXT NOT NULL UNIQUE,
	username TEXT NOT NULL,
	secret   TEXT NOT NULL
);

-- scope is how much of the forge a token answers for: a whole host
-- ("github.com"), one owner on it ("github.com/acme"), a single repository, or
-- '*' for everything left over. It is unique because a clone URL resolves to
-- exactly one credential — the most specific scope that covers it.
CREATE TABLE IF NOT EXISTS git_credentials (
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	name         TEXT NOT NULL DEFAULT '',
	scope        TEXT NOT NULL UNIQUE,
	username     TEXT NOT NULL DEFAULT '',
	secret       TEXT NOT NULL,
	hint         TEXT NOT NULL DEFAULT '',
	created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	last_used_at DATETIME
);

CREATE TABLE IF NOT EXISTS settings (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);

-- An operator's own catalogue of one-click entries, kept as the YAML document
-- it was written or imported as. position is the merge order: a later
-- catalogue's entry replaces an earlier one with the same id, and any of them
-- replaces the built-in entry of that id.
CREATE TABLE IF NOT EXISTS catalogs (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	name       TEXT NOT NULL,
	source_url TEXT NOT NULL DEFAULT '',
	yaml       TEXT NOT NULL,
	enabled    INTEGER NOT NULL DEFAULT 1,
	position   INTEGER NOT NULL DEFAULT 0,
	updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- An installed station: an application that arrives with a control surface of
-- its own, kept as the YAML document it was pasted or fetched as. station_id
-- is the document's own id and is unique, because a station is a program and
-- one must not silently replace another.
--
-- Three revision columns rather than a revisions table: yaml is what every
-- application running this station is served from, prev_yaml is the one click
-- back from a bad update, and pending_yaml is a revision that has been fetched
-- and is waiting to be approved because its permissions changed.
CREATE TABLE IF NOT EXISTS stations (
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	station_id   TEXT NOT NULL UNIQUE,
	name         TEXT NOT NULL,
	source_url   TEXT NOT NULL DEFAULT '',
	yaml         TEXT NOT NULL,
	perms_hash   TEXT NOT NULL,
	prev_yaml    TEXT NOT NULL DEFAULT '',
	pending_yaml TEXT NOT NULL DEFAULT '',
	pending_hash TEXT NOT NULL DEFAULT '',
	enabled      INTEGER NOT NULL DEFAULT 1,
	favorite     INTEGER NOT NULL DEFAULT 0,
	position     INTEGER NOT NULL DEFAULT 0,
	updated_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- The small key-value space a station gets, scoped to one application and one
-- station. It needs no permission because it can reach nothing else — but it
-- does need a table, because a station's script runs in a process that holds
-- no disk and nothing in it survives the call.
CREATE TABLE IF NOT EXISTS station_store (
	app_id     TEXT NOT NULL,
	station_id TEXT NOT NULL,
	key        TEXT NOT NULL,
	value      TEXT NOT NULL,
	updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	PRIMARY KEY (app_id, station_id, key)
);

-- What a station measured about its own application, over time. The store is
-- for what an action worked out and needs next time; this is for the one thing
-- the store cannot hold, which is a value on Tuesday and the same value on
-- Wednesday. A station samples it from a scheduled hook and a chart panel
-- reads it back, so the history belongs to Quasar rather than to a script that
-- gets 256 KB and no clock.
CREATE TABLE IF NOT EXISTS station_series (
	app_id     TEXT NOT NULL,
	station_id TEXT NOT NULL,
	name       TEXT NOT NULL,
	ts         DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	value      REAL NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_station_series ON station_series(app_id, station_id, name, ts);

-- The same series once they are too old to keep a sample a minute of. An hour
-- becomes one row, and the row keeps the least, the most and the mean rather
-- than the mean alone: an average hour hides the spike that was the reason
-- anybody opened the graph, and once the samples are folded there is no
-- getting it back.
CREATE TABLE IF NOT EXISTS station_series_hourly (
	app_id     TEXT NOT NULL,
	station_id TEXT NOT NULL,
	name       TEXT NOT NULL,
	hour       DATETIME NOT NULL,
	avg_value  REAL NOT NULL,
	min_value  REAL NOT NULL,
	max_value  REAL NOT NULL,
	samples    INTEGER NOT NULL,
	PRIMARY KEY (app_id, station_id, name, hour)
);

CREATE TABLE IF NOT EXISTS api_tokens (
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	name         TEXT NOT NULL,
	role         TEXT NOT NULL DEFAULT 'viewer',
	prefix       TEXT NOT NULL DEFAULT '',
	token_hash   TEXT NOT NULL UNIQUE,
	created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	last_used_at DATETIME
);

CREATE TABLE IF NOT EXISTS audit_log (
	id     INTEGER PRIMARY KEY AUTOINCREMENT,
	ts     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	actor  TEXT NOT NULL,
	action TEXT NOT NULL,
	target TEXT NOT NULL DEFAULT '',
	detail TEXT NOT NULL DEFAULT '',
	ip     TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_audit_ts ON audit_log(ts);
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
	"ALTER TABLE apps ADD COLUMN sort_order INTEGER NOT NULL DEFAULT 0",
	"ALTER TABLE apps ADD COLUMN pre_backup_cmd TEXT NOT NULL DEFAULT ''",
	"ALTER TABLE apps ADD COLUMN rate_limit INTEGER NOT NULL DEFAULT 0",
	"ALTER TABLE apps ADD COLUMN ip_allow_cidrs TEXT NOT NULL DEFAULT ''",
	"ALTER TABLE apps ADD COLUMN security_headers INTEGER NOT NULL DEFAULT 0",
	// Existing single-account installs default to admin, so an upgrade does
	// not lock anyone out of their own dashboard.
	"ALTER TABLE users ADD COLUMN role TEXT NOT NULL DEFAULT 'admin'",
	// Empty means "whatever the repository asks for", so existing git apps
	// pick up compose support without anyone having to choose.
	"ALTER TABLE apps ADD COLUMN git_build TEXT NOT NULL DEFAULT ''",
	// Empty means "whichever service Quasar works out from the compose file",
	// which is what every existing stack has been running on all along.
	"ALTER TABLE apps ADD COLUMN compose_service TEXT NOT NULL DEFAULT ''",
	// A credential started out naming only a host; it can now name an owner or
	// a single repository on one, and every existing value is still a valid
	// scope — the widest kind.
	"ALTER TABLE git_credentials RENAME COLUMN host TO scope",
	// Empty means "not deployed from a station", which every application that
	// existed before stations did is.
	"ALTER TABLE apps ADD COLUMN station_id TEXT NOT NULL DEFAULT ''",
	// The choices a station was deployed with, which quasar.app.params reads
	// back. Empty for everything that was not deployed from a station.
	"ALTER TABLE apps ADD COLUMN station_params TEXT NOT NULL DEFAULT ''",
	// Nothing is starred until somebody stars it, which is the right answer
	// for an install that already has its stations.
	"ALTER TABLE stations ADD COLUMN favorite INTEGER NOT NULL DEFAULT 0",
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
		_ = db.Close() // the schema error is the one worth returning
		return nil, err
	}
	for _, m := range migrations {
		// A migration that has already run is the expected answer on every
		// database but a brand new one. Anything else is logged rather than
		// fatal: refusing to start would lock an operator out of the dashboard
		// they need in order to fix it.
		if _, err := db.Exec(m); err != nil && !alreadyApplied(err) {
			log.Printf("db: migration %q: %v", m, err)
		}
	}
	return db, nil
}

// alreadyApplied reports the two ways SQLite says a migration has already run.
// An ADD COLUMN whose column is there answers "duplicate column name"; a RENAME
// COLUMN that already happened cannot find the name it was told to rename.
func alreadyApplied(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "duplicate column") || strings.Contains(msg, "no such column")
}
