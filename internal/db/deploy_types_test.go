package db

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// A database from before "local" existed is rebuilt on open: its apps keep
// every column, and a local application can then be stored.
func TestOpenAllowsLocalOnAnOlderDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	// The apps table as an older Quasar created it: the old check, and
	// without the columns added by migrations since.
	old := `CREATE TABLE apps (
	id             TEXT PRIMARY KEY,
	name           TEXT NOT NULL,
	subdomain      TEXT NOT NULL UNIQUE,
	deploy_type    TEXT NOT NULL ` + oldDeployTypes + `,
	image_ref      TEXT NOT NULL DEFAULT '',
	git_url        TEXT NOT NULL DEFAULT '',
	git_branch     TEXT NOT NULL DEFAULT 'main',
	compose_yaml   TEXT NOT NULL DEFAULT '',
	port           INTEGER NOT NULL DEFAULT 80,
	env_content    TEXT NOT NULL DEFAULT '',
	data_mount     TEXT NOT NULL DEFAULT '',
	created_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
)`
	if _, err := raw.Exec(old); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO apps (id, name, subdomain, deploy_type, image_ref) VALUES ('a1', 'Blog', 'blog', 'image', 'nginx')`); err != nil {
		t.Fatal(err)
	}
	raw.Close()

	database, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var def string
	if err := database.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'apps'`).Scan(&def); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(def, "'local'") || !strings.Contains(def, "log_color") {
		t.Errorf("apps table after open: %s", def)
	}
	k := testKeyring(t)
	a, err := GetApp(database, k, "a1")
	if err != nil || a.ImageRef != "nginx" || a.Subdomain != "blog" {
		t.Fatalf("the existing app after the rebuild: %+v, %v", a, err)
	}
	if err := InsertApp(database, k, &App{ID: "a2", Name: "Site", Subdomain: "site", DeployType: "local"}); err != nil {
		t.Fatalf("storing a local app: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO apps (id, name, subdomain, deploy_type) VALUES ('a3', 'x', 'x', 'ftp')`); err == nil {
		t.Error("the check should still refuse an unknown type")
	}

	// And a second open, of a table already rebuilt, changes nothing.
	if err := allowLocalDeployType(database); err != nil {
		t.Fatal(err)
	}
}
