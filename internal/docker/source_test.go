package docker

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"quasar/internal/db"
)

// originRepo builds a real repository to clone from, so the test exercises git
// itself rather than a stand-in for it.
func originRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
	}
	run("init", "-b", "main")
	writeVersion(t, dir, "v1")
	run("add", ".")
	run("commit", "-m", "v1")
	return dir
}

func writeVersion(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "VERSION"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func commitVersion(t *testing.T, dir, content string) {
	t.Helper()
	writeVersion(t, dir, content)
	cmd := exec.Command("git", "-C", dir, "commit", "-am", content)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("commit: %s: %v", out, err)
	}
}

func checkedOut(t *testing.T, src string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(src, "VERSION"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// The distinction the two buttons rest on: a redeploy rebuilds the commit
// already on disk, an update advances to the branch head. If a redeploy pulled
// too, pushing to the branch would silently change what the next configuration
// change deploys.
func TestSyncSourceOnlyPullsWhenAskedTo(t *testing.T) {
	origin := originRepo(t)
	c := &Client{appsDir: t.TempDir()}
	a := &db.App{ID: "a1", DeployType: "git", GitURL: origin, GitBranch: "main"}
	ctx := context.Background()

	// Nothing checked out yet: it has to clone even without an update.
	src, err := c.syncSource(ctx, a, false)
	if err != nil {
		t.Fatalf("initial syncSource: %v", err)
	}
	if got := checkedOut(t, src); got != "v1" {
		t.Fatalf("after clone, VERSION = %q, want v1", got)
	}

	commitVersion(t, origin, "v2")

	if _, err := c.syncSource(ctx, a, false); err != nil {
		t.Fatalf("syncSource without pull: %v", err)
	}
	if got := checkedOut(t, src); got != "v1" {
		t.Errorf("a redeploy moved the checkout to %q, want it left on v1", got)
	}

	if _, err := c.syncSource(ctx, a, true); err != nil {
		t.Fatalf("syncSource with pull: %v", err)
	}
	if got := checkedOut(t, src); got != "v2" {
		t.Errorf("an update left the checkout on %q, want v2", got)
	}
}

// An ssh remote, or one that already carries credentials, must not cost a
// credential lookup — which is what lets a deploy run against a plain path
// with no database behind it at all, as the test above does.
func TestGitCredentialLookupSkippedWithoutADatabase(t *testing.T) {
	c := &Client{} // no database: the cheap checks have to come first
	for _, url := range []string{
		"git@github.com:owner/repo.git",
		"ssh://git@example.com/repo.git",
		"https://oauth2:secret@github.com/owner/repo.git",
		"https://github.com/owner/repo.git",
	} {
		if cred := c.gitCredentialFor(url); cred != nil {
			t.Errorf("gitCredentialFor(%q) returned a credential with no database", url)
		}
	}
}

// gitIn runs git in dir with an identity, for setting up an origin.
func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %s: %v", args, out, err)
	}
}

// Moving an app to another repository has to start again from that one: a
// redeploy rebuilding the old checkout, or an update pulling it, would carry
// on running the code the app was just moved away from.
func TestSyncSourceClonesAgainForAnotherRepository(t *testing.T) {
	v1, v2 := originRepo(t), originRepo(t)
	commitVersion(t, v2, "portfolio v2")
	c := &Client{appsDir: t.TempDir()}
	a := &db.App{ID: "a1", DeployType: "git", GitURL: v1, GitBranch: "main"}
	ctx := context.Background()

	src, err := c.syncSource(ctx, a, false)
	if err != nil {
		t.Fatalf("initial syncSource: %v", err)
	}
	a.GitURL = v2
	if _, err := c.syncSource(ctx, a, false); err != nil {
		t.Fatalf("syncSource after the move: %v", err)
	}
	if got := checkedOut(t, src); got != "portfolio v2" {
		t.Errorf("after moving to another repository, VERSION = %q, want the new one", got)
	}
}

func TestSyncSourceClonesAgainForAnotherBranch(t *testing.T) {
	origin := originRepo(t)
	gitIn(t, origin, "checkout", "-b", "next")
	commitVersion(t, origin, "next")
	gitIn(t, origin, "checkout", "main")
	c := &Client{appsDir: t.TempDir()}
	a := &db.App{ID: "a1", DeployType: "git", GitURL: origin, GitBranch: "main"}
	ctx := context.Background()

	src, err := c.syncSource(ctx, a, false)
	if err != nil {
		t.Fatalf("initial syncSource: %v", err)
	}
	a.GitBranch = "next"
	if _, err := c.syncSource(ctx, a, true); err != nil {
		t.Fatalf("syncSource after the switch: %v", err)
	}
	if got := checkedOut(t, src); got != "next" {
		t.Errorf("after switching branch, VERSION = %q, want next", got)
	}
}

// A new source that cannot be cloned fails the deploy, and leaves the checkout
// the running app came from in place.
func TestSyncSourceKeepsTheCheckoutWhenTheNewOneFails(t *testing.T) {
	origin := originRepo(t)
	c := &Client{appsDir: t.TempDir()}
	a := &db.App{ID: "a1", DeployType: "git", GitURL: origin, GitBranch: "main"}
	ctx := context.Background()

	src, err := c.syncSource(ctx, a, false)
	if err != nil {
		t.Fatalf("initial syncSource: %v", err)
	}
	a.GitBranch = "no-such-branch"
	if _, err := c.syncSource(ctx, a, true); err == nil {
		t.Fatal("syncSource of a branch that does not exist succeeded")
	}
	if got := checkedOut(t, src); got != "v1" {
		t.Errorf("the failed clone left VERSION = %q, want the old checkout's v1", got)
	}
	if _, err := os.Stat(src + ".next"); !os.IsNotExist(err) {
		t.Errorf("the failed clone left its staging directory behind: %v", err)
	}
}

// A tag leaves no branch checked out, which must not read as a mismatch —
// otherwise every redeploy of an app pinned to a tag would clone it again.
func TestSyncSourceKeepsATagCheckout(t *testing.T) {
	origin := originRepo(t)
	gitIn(t, origin, "tag", "v1.0")
	c := &Client{appsDir: t.TempDir()}
	a := &db.App{ID: "a1", DeployType: "git", GitURL: origin, GitBranch: "v1.0"}
	ctx := context.Background()

	src, err := c.syncSource(ctx, a, false)
	if err != nil {
		t.Fatalf("initial syncSource: %v", err)
	}
	if why := checkoutMismatch(ctx, src, a); why != "" {
		t.Errorf("a checkout of the tag it was cloned from reads as a mismatch: %s", why)
	}
}

func TestCheckGitSource(t *testing.T) {
	origin := originRepo(t)
	gitIn(t, origin, "tag", "v1.0")
	gitIn(t, origin, "branch", "feature/main")
	c := &Client{}
	ctx := context.Background()

	for _, ref := range []string{"main", "v1.0", "feature/main"} {
		if err := c.CheckGitSource(ctx, origin, ref); err != nil {
			t.Errorf("CheckGitSource(%q): %v", ref, err)
		}
	}
	// "main" is also the tail of "feature/main"; neither is taken for the
	// other, and a name that is only a suffix is not found.
	if err := c.CheckGitSource(ctx, origin, "ain"); err == nil {
		t.Error("CheckGitSource accepted a branch that does not exist")
	}
	if err := c.CheckGitSource(ctx, filepath.Join(t.TempDir(), "missing"), "main"); err == nil {
		t.Error("CheckGitSource accepted a repository that does not exist")
	}
}
