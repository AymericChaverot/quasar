package db

import "testing"

func TestEncryptLegacyApps(t *testing.T) {
	database := openTestDB(t)
	keyring := testKeyring(t)

	// One app created before encryption existed: plaintext columns, written
	// directly (bypassing InsertApp, which would already encrypt).
	if err := InsertApp(database, keyring, &App{ID: "legacy", Name: "Legacy", Subdomain: "legacy", DeployType: "compose"}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec("UPDATE apps SET env_content = ?, compose_yaml = ? WHERE id = ?",
		"SECRET=hunter2", "services:\n  web:\n", "legacy"); err != nil {
		t.Fatal(err)
	}

	// A second app already encrypted (e.g. created after the upgrade) must
	// be left untouched, not double-encrypted.
	if err := InsertApp(database, keyring, &App{ID: "fresh", Name: "Fresh", Subdomain: "fresh", DeployType: "image", EnvContent: "FRESH=1"}); err != nil {
		t.Fatal(err)
	}
	var freshBefore string
	database.QueryRow("SELECT env_content FROM apps WHERE id = ?", "fresh").Scan(&freshBefore)

	n, err := EncryptLegacyApps(database, keyring)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("EncryptLegacyApps touched %d rows, want 1", n)
	}

	var legacyRaw string
	database.QueryRow("SELECT env_content FROM apps WHERE id = ?", "legacy").Scan(&legacyRaw)
	if legacyRaw == "SECRET=hunter2" {
		t.Error("legacy row still stored in plaintext after migration")
	}

	got, err := GetApp(database, keyring, "legacy")
	if err != nil {
		t.Fatal(err)
	}
	if got.EnvContent != "SECRET=hunter2" {
		t.Errorf("GetApp(legacy).EnvContent = %q, want SECRET=hunter2", got.EnvContent)
	}
	if got.ComposeYAML != "services:\n  web:\n" {
		t.Errorf("GetApp(legacy).ComposeYAML = %q, want original", got.ComposeYAML)
	}

	var freshAfter string
	database.QueryRow("SELECT env_content FROM apps WHERE id = ?", "fresh").Scan(&freshAfter)
	if freshAfter != freshBefore {
		t.Errorf("already-encrypted row was rewritten (possible double-encryption): before=%q after=%q", freshBefore, freshAfter)
	}
}
