package db

import (
	"strings"
	"testing"
)

func TestAppEnvIsEncryptedAtRest(t *testing.T) {
	database := openTestDB(t)
	keyring := testKeyring(t)

	a := &App{
		ID: "app1", Name: "Web", Subdomain: "web", DeployType: "compose",
		EnvContent:  "SECRET_TOKEN=hunter2",
		ComposeYAML: "services:\n  web:\n    environment:\n      SECRET_TOKEN: hunter2\n",
	}
	if err := InsertApp(database, keyring, a); err != nil {
		t.Fatal(err)
	}

	// The raw column must not contain the plaintext secret.
	var rawEnv, rawCompose string
	if err := database.QueryRow("SELECT env_content, compose_yaml FROM apps WHERE id = ?", a.ID).Scan(&rawEnv, &rawCompose); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rawEnv, "hunter2") {
		t.Errorf("env_content stored in plaintext: %q", rawEnv)
	}
	if strings.Contains(rawCompose, "hunter2") {
		t.Errorf("compose_yaml stored in plaintext: %q", rawCompose)
	}
	if !strings.HasPrefix(rawEnv, "enc:v1:") {
		t.Errorf("env_content missing enc:v1: prefix: %q", rawEnv)
	}

	// GetApp and ListApps must transparently decrypt back to plaintext.
	got, err := GetApp(database, keyring, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.EnvContent != a.EnvContent {
		t.Errorf("GetApp EnvContent = %q, want %q", got.EnvContent, a.EnvContent)
	}
	if got.ComposeYAML != a.ComposeYAML {
		t.Errorf("GetApp ComposeYAML = %q, want %q", got.ComposeYAML, a.ComposeYAML)
	}

	list, err := ListApps(database, keyring)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].EnvContent != a.EnvContent {
		t.Fatalf("ListApps did not decrypt EnvContent: %+v", list)
	}

	// UpdateAppEnv must re-encrypt.
	if err := UpdateAppEnv(database, keyring, a.ID, "OTHER=value"); err != nil {
		t.Fatal(err)
	}
	var rawEnv2 string
	database.QueryRow("SELECT env_content FROM apps WHERE id = ?", a.ID).Scan(&rawEnv2)
	if strings.Contains(rawEnv2, "OTHER=value") {
		t.Errorf("updated env_content stored in plaintext: %q", rawEnv2)
	}
	got2, err := GetApp(database, keyring, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got2.EnvContent != "OTHER=value" {
		t.Errorf("GetApp after update = %q, want OTHER=value", got2.EnvContent)
	}
}

func TestAppEnvPlaintextBackwardCompat(t *testing.T) {
	database := openTestDB(t)
	keyring := testKeyring(t)

	// Simulate a row written before encryption existed: plaintext, no prefix.
	if err := InsertApp(database, keyring, &App{ID: "legacy", Name: "Legacy", Subdomain: "legacy", DeployType: "image", ImageRef: "nginx"}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec("UPDATE apps SET env_content = ? WHERE id = ?", "PLAIN=1", "legacy"); err != nil {
		t.Fatal(err)
	}

	got, err := GetApp(database, keyring, "legacy")
	if err != nil {
		t.Fatal(err)
	}
	if got.EnvContent != "PLAIN=1" {
		t.Errorf("EnvContent = %q, want PLAIN=1 (unprefixed legacy value passed through)", got.EnvContent)
	}
}
