package backup

import (
	"database/sql"
	"path/filepath"
	"testing"

	"quasar/internal/db"
	"quasar/internal/secrets"
)

// host stands in for one Quasar install: its own database and its own master
// key, which is what makes a cross-host restore the interesting case.
type host struct {
	db      *sql.DB
	keyring *secrets.Keyring
	appsDir string
	backups string
}

func newHost(t *testing.T) *host {
	t.Helper()
	root := t.TempDir()
	database, err := db.Open(filepath.Join(root, "storage", "database.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	keyring, err := secrets.LoadOrCreateKey(filepath.Join(root, "storage", "master.key"))
	if err != nil {
		t.Fatal(err)
	}
	return &host{
		db:      database,
		keyring: keyring,
		appsDir: filepath.Join(root, "apps"),
		backups: filepath.Join(root, "backups"),
	}
}

const secretEnv = "DATABASE_URL=postgres://user:hunter2@db:5432/app\nAPI_KEY=sk-live-abc123"

// Rebuilding a dead VPS is the case backups exist for, and the master key is
// deliberately not in the archive — so restoring has to be given that key or
// every app comes back with unreadable configuration.
func TestRestoreAcrossHostsWithArchiveKey(t *testing.T) {
	source := newHost(t)
	if err := db.InsertApp(source.db, source.keyring, &db.App{
		ID: "web", Name: "Web", Subdomain: "web", DeployType: "image",
		ImageRef: "nginx", EnvContent: secretEnv,
	}); err != nil {
		t.Fatal(err)
	}
	name, err := Run(source.db, source.appsDir, source.backups)
	if err != nil {
		t.Fatal(err)
	}

	// The rebuilt server: a fresh install, so a brand new master key.
	rebuilt := newHost(t)
	copyArchive(t, filepath.Join(source.backups, name), filepath.Join(rebuilt.backups, name))

	if err := Restore(rebuilt.db, rebuilt.appsDir, rebuilt.backups, name,
		rebuilt.keyring, source.keyring); err != nil {
		t.Fatalf("restore with the archive's key: %v", err)
	}

	restored, err := db.GetApp(rebuilt.db, rebuilt.keyring, "web")
	if err != nil {
		t.Fatalf("reading the restored app with the live key: %v", err)
	}
	if restored.EnvContent != secretEnv {
		t.Errorf("env content came back as %q, want %q", restored.EnvContent, secretEnv)
	}
}

// The same restore without the key must fail loudly rather than leave apps
// holding ciphertext nobody can open.
func TestRestoreAcrossHostsWithoutKeyLeavesSecretsUnreadable(t *testing.T) {
	source := newHost(t)
	if err := db.InsertApp(source.db, source.keyring, &db.App{
		ID: "web", Name: "Web", Subdomain: "web", DeployType: "image",
		ImageRef: "nginx", EnvContent: secretEnv,
	}); err != nil {
		t.Fatal(err)
	}
	name, err := Run(source.db, source.appsDir, source.backups)
	if err != nil {
		t.Fatal(err)
	}

	rebuilt := newHost(t)
	copyArchive(t, filepath.Join(source.backups, name), filepath.Join(rebuilt.backups, name))

	if err := Restore(rebuilt.db, rebuilt.appsDir, rebuilt.backups, name, rebuilt.keyring, nil); err != nil {
		t.Fatalf("restore itself should succeed: %v", err)
	}

	// The row itself did land — the unencrypted columns prove the restore ran,
	// so the failure below is really about the key and not a missing app.
	var restoredName string
	if err := rebuilt.db.QueryRow("SELECT name FROM apps WHERE id = 'web'").Scan(&restoredName); err != nil {
		t.Fatalf("the app row should have been restored: %v", err)
	}
	if restoredName != "Web" {
		t.Fatalf("restored app name = %q, want Web", restoredName)
	}
	if _, err := db.GetApp(rebuilt.db, rebuilt.keyring, "web"); err == nil {
		t.Error("expected the app's secrets to be unreadable without the source key")
	}
}

// Restoring an archive from this same install needs no key at all.
func TestRestoreSameHostNeedsNoKey(t *testing.T) {
	h := newHost(t)
	if err := db.InsertApp(h.db, h.keyring, &db.App{
		ID: "web", Name: "Web", Subdomain: "web", DeployType: "image",
		ImageRef: "nginx", EnvContent: secretEnv,
	}); err != nil {
		t.Fatal(err)
	}
	name, err := Run(h.db, h.appsDir, h.backups)
	if err != nil {
		t.Fatal(err)
	}

	// Lose the app, then get it back from the archive.
	if _, err := h.db.Exec("DELETE FROM apps"); err != nil {
		t.Fatal(err)
	}
	if err := Restore(h.db, h.appsDir, h.backups, name, h.keyring, nil); err != nil {
		t.Fatal(err)
	}
	restored, err := db.GetApp(h.db, h.keyring, "web")
	if err != nil {
		t.Fatal(err)
	}
	if restored.EnvContent != secretEnv {
		t.Errorf("env content came back as %q, want %q", restored.EnvContent, secretEnv)
	}
}

// A wrong key must be rejected outright, not silently written over the rows.
func TestRestoreWithWrongKeyFails(t *testing.T) {
	source := newHost(t)
	if err := db.InsertApp(source.db, source.keyring, &db.App{
		ID: "web", Name: "Web", Subdomain: "web", DeployType: "image",
		ImageRef: "nginx", EnvContent: secretEnv,
	}); err != nil {
		t.Fatal(err)
	}
	name, err := Run(source.db, source.appsDir, source.backups)
	if err != nil {
		t.Fatal(err)
	}

	rebuilt := newHost(t)
	copyArchive(t, filepath.Join(source.backups, name), filepath.Join(rebuilt.backups, name))
	unrelated := newHost(t) // a third install's key, i.e. the wrong one

	err = Restore(rebuilt.db, rebuilt.appsDir, rebuilt.backups, name, rebuilt.keyring, unrelated.keyring)
	if err == nil {
		t.Fatal("expected the restore to reject a key that cannot open the archive")
	}
}

func copyArchive(t *testing.T, src, dst string) {
	t.Helper()
	if err := copyFile(src, dst, 0o644); err != nil {
		t.Fatal(err)
	}
}
