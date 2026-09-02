package db

import (
	"testing"
)

// A stack has no image tag to go back to, so what it goes back to is the file
// it ran — which means the file has to be kept, kept encrypted, and kept
// against the application it belongs to.
func TestADeploymentKeepsTheComposeItRan(t *testing.T) {
	database := openTestDB(t)
	keyring := testKeyring(t)

	const compose = "services:\n  db:\n    image: postgres:16\n    environment:\n      POSTGRES_PASSWORD: hunter2\n"
	id := StartDeployment(database, keyring, "app1", "manual", compose)
	if id == 0 {
		t.Fatal("the deployment was not recorded")
	}

	// At rest it is ciphertext. A compose file carries an environment block,
	// and that is where the passwords are.
	var stored string
	if err := database.QueryRow("SELECT compose_yaml FROM deployments WHERE id = ?", id).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == compose || stored == "" {
		t.Error("the compose file was stored in the clear")
	}

	got, err := DeploymentCompose(database, keyring, "app1", id)
	if err != nil {
		t.Fatal(err)
	}
	if got != compose {
		t.Errorf("the file came back as %q", got)
	}

	// The id travels in a form, so one application's history must not be a way
	// to read another's file.
	if _, err := DeploymentCompose(database, keyring, "app2", id); err == nil {
		t.Error("a deployment of another application handed over its compose file")
	}

	// And a deployment from before any of this was recorded says so rather
	// than handing back an empty file that would deploy as nothing.
	plain := StartDeployment(database, keyring, "app1", "manual", "")
	if _, err := DeploymentCompose(database, keyring, "app1", plain); err == nil {
		t.Error("a deployment that kept no file pretended it had one")
	}
}

// The list draws a table and must not decrypt a page of secrets to do it; what
// it needs to know is whether there is a file to go back to.
func TestTheListSaysWhetherThereIsAFileWithoutReadingIt(t *testing.T) {
	database := openTestDB(t)
	keyring := testKeyring(t)

	StartDeployment(database, keyring, "app1", "manual", "services: {}\n")
	StartDeployment(database, keyring, "app1", "manual", "")

	deps, err := ListDeployments(database, "app1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(deps) != 2 {
		t.Fatalf("got %d deployments, want 2", len(deps))
	}
	// Newest first.
	if deps[0].HasCompose {
		t.Error("a deployment with no file says it has one")
	}
	if !deps[1].HasCompose {
		t.Error("a deployment with a file says it has none")
	}
}
