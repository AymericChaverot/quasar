package docker

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"quasar/internal/db"
)

// `env_file: .env` is how a repository written to run standalone reads its
// configuration, and it resolves against the checkout — not against the app
// directory where Quasar keeps the canonical copy. Without this the dashboard's
// environment editor would configure nothing.
func TestWriteSourceEnvLandsBesideTheComposeFile(t *testing.T) {
	c := &Client{appsDir: t.TempDir()}
	a := &db.App{ID: "a1", DeployType: "git", EnvContent: "TOKEN=s3cret\nPORT=8080"}
	src := c.sourceDir(a.ID)
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := c.writeSourceEnv(context.Background(), src, a); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(src, ".env"))
	if err != nil {
		t.Fatalf("no .env in the checkout: %v", err)
	}
	if want := "TOKEN=s3cret\nPORT=8080\n"; string(got) != want {
		t.Errorf(".env = %q, want %q", got, want)
	}
}

// A .env the author committed is theirs. Overwriting it would discard it and
// dirty the working tree, which is what makes the next `git pull --ff-only`
// refuse to advance — turning every later update into a failed deploy.
func TestWriteSourceEnvLeavesATrackedFileAlone(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	c := &Client{appsDir: t.TempDir()}
	a := &db.App{ID: "a1", DeployType: "git", EnvContent: "FROM_DASHBOARD=1"}
	src := c.sourceDir(a.ID)
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}

	const committed = "FROM_THE_REPO=1\n"
	if err := os.WriteFile(filepath.Join(src, ".env"), []byte(committed), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", src}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
	}
	run("init", "-b", "main")
	run("add", ".env")
	run("commit", "-m", "env")

	if err := c.writeSourceEnv(context.Background(), src, a); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(src, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != committed {
		t.Errorf("a tracked .env was overwritten: %q", got)
	}
}
