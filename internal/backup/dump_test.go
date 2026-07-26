package backup

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"quasar/internal/db"
)

const dumpSQL = "--\n-- PostgreSQL database dump\n--\nCREATE TABLE users (id int);\n"

func withDatabaseApp(t *testing.T, h *host, preBackup string) {
	t.Helper()
	if err := db.InsertApp(h.db, h.keyring, &db.App{
		ID: "pg", Name: "Postgres", Subdomain: "pg", DeployType: "image",
		ImageRef: "postgres:16", DataMount: "/var/lib/postgresql/data",
		PreBackupCmd: preBackup,
	}); err != nil {
		t.Fatal(err)
	}
}

// The dump is the copy worth restoring, so it has to survive the round trip
// into the archive and back onto disk.
func TestDumpIsArchivedAndRestored(t *testing.T) {
	h := newHost(t)
	withDatabaseApp(t, h, "pg_dump -U postgres app")

	var ranOn string
	dump := func(a *db.App) ([]byte, error) {
		ranOn = a.PreBackupCmd
		return []byte(dumpSQL), nil
	}

	name, err := Run(h.db, h.keyring, h.appsDir, h.backups, dump)
	if err != nil {
		t.Fatal(err)
	}
	if ranOn != "pg_dump -U postgres app" {
		t.Errorf("the stored command reached the dumper as %q", ranOn)
	}

	if err := Restore(h.db, h.appsDir, h.backups, name, h.keyring, nil); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(h.appsDir, "pg", "dump.sql"))
	if err != nil {
		t.Fatalf("dump.sql should be restored beside the data directory: %v", err)
	}
	if string(got) != dumpSQL {
		t.Errorf("restored dump = %q, want %q", got, dumpSQL)
	}
}

// A backup that quietly dropped the one artifact making it restorable would be
// worse than no backup, so the whole run fails.
func TestFailingDumpFailsTheBackup(t *testing.T) {
	h := newHost(t)
	withDatabaseApp(t, h, "pg_dump -U wrong app")

	dump := func(*db.App) ([]byte, error) {
		return nil, errors.New(`exit code 1: pg_dump: error: role "wrong" does not exist`)
	}

	_, err := Run(h.db, h.keyring, h.appsDir, h.backups, dump)
	if err == nil {
		t.Fatal("expected the backup to fail when the dump command fails")
	}
	if !strings.Contains(err.Error(), "Postgres") {
		t.Errorf("the error should name the app, got %q", err)
	}

	// And it must not leave a half-written archive that looks usable.
	if got := List(h.backups); len(got) != 0 {
		t.Errorf("a failed backup left %d archive(s) behind", len(got))
	}
}

// A stopped app is not writing, so its files are already consistent — that is
// not a reason to fail every other app's backup.
func TestStoppedAppSkipsDumpWithoutFailing(t *testing.T) {
	h := newHost(t)
	withDatabaseApp(t, h, "pg_dump -U postgres app")

	// nil output with a nil error is how the dumper reports "not running".
	dump := func(*db.App) ([]byte, error) { return nil, nil }

	name, err := Run(h.db, h.keyring, h.appsDir, h.backups, dump)
	if err != nil {
		t.Fatalf("a skipped dump must not fail the backup: %v", err)
	}
	if err := Restore(h.db, h.appsDir, h.backups, name, h.keyring, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(h.appsDir, "pg", "dump.sql")); !os.IsNotExist(err) {
		t.Error("no dump should have been written")
	}
}

// Apps with no command configured must not invoke the dumper at all.
func TestAppWithoutCommandIsNotDumped(t *testing.T) {
	h := newHost(t)
	withDatabaseApp(t, h, "")

	called := false
	dump := func(*db.App) ([]byte, error) {
		called = true
		return []byte("unexpected"), nil
	}
	if _, err := Run(h.db, h.keyring, h.appsDir, h.backups, dump); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Error("the dumper ran for an app with no pre-backup command")
	}
}

// The command can carry credentials, so it is encrypted like env content.
func TestPreBackupCommandIsEncryptedAtRest(t *testing.T) {
	h := newHost(t)
	const cmd = "mysqldump -u root -phunter2 app"
	withDatabaseApp(t, h, cmd)

	var stored string
	if err := h.db.QueryRow("SELECT pre_backup_cmd FROM apps WHERE id = 'pg'").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, "hunter2") {
		t.Error("the command is stored in plaintext, credentials and all")
	}

	a, err := db.GetApp(h.db, h.keyring, "pg")
	if err != nil {
		t.Fatal(err)
	}
	if a.PreBackupCmd != cmd {
		t.Errorf("command round-tripped as %q, want %q", a.PreBackupCmd, cmd)
	}
}
