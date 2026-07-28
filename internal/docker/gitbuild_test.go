package docker

import (
	"os"
	"path/filepath"
	"testing"

	"quasar/internal/db"
)

// checkout writes the named files into an app's source directory, as a clone
// would leave them.
func checkout(t *testing.T, c *Client, appID string, names ...string) {
	t.Helper()
	src := c.sourceDir(appID)
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(src, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// What the whole feature rests on: a repository that carries a compose file is
// a stack, and building the Dockerfile beside it would run one service of that
// stack and report the app as deployed.
func TestGitBuildPrefersCompose(t *testing.T) {
	tests := []struct {
		name   string
		files  []string
		choice string
		want   string
	}{
		{"compose alone", []string{"docker-compose.yml"}, db.GitBuildAuto, db.GitBuildCompose},
		{"dockerfile alone", []string{"Dockerfile"}, db.GitBuildAuto, db.GitBuildDockerfile},
		{"both, undecided", []string{"Dockerfile", "docker-compose.yml"}, db.GitBuildAuto, db.GitBuildCompose},
		{"both, dockerfile asked for", []string{"Dockerfile", "docker-compose.yml"}, db.GitBuildDockerfile, db.GitBuildDockerfile},
		{"both, compose asked for", []string{"Dockerfile", "docker-compose.yml"}, db.GitBuildCompose, db.GitBuildCompose},
		// An explicit choice the checkout cannot honour must not deadlock the
		// app: the repository is deployed the only way it can be.
		{"dockerfile asked for, none there", []string{"compose.yaml"}, db.GitBuildDockerfile, db.GitBuildCompose},
		{"compose asked for, none there", []string{"Dockerfile"}, db.GitBuildCompose, db.GitBuildDockerfile},
		{"empty repository", nil, db.GitBuildAuto, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := &Client{appsDir: t.TempDir()}
			a := &db.App{ID: "a1", DeployType: "git", GitBuild: tc.choice}
			checkout(t, c, a.ID, tc.files...)

			b := c.GitBuildFor(a)
			if b.Mode != tc.want {
				t.Errorf("Mode = %q, want %q", b.Mode, tc.want)
			}
			if got := c.UsesCompose(a); got != (tc.want == db.GitBuildCompose) {
				t.Errorf("UsesCompose() = %v, want %v", got, tc.want == db.GitBuildCompose)
			}
		})
	}
}

// The panel says why an app is not built the way it was told to be, which it
// can only do if the mismatch is reported rather than silently resolved.
func TestGitBuildReportsAnImpossibleChoice(t *testing.T) {
	c := &Client{appsDir: t.TempDir()}
	a := &db.App{ID: "a1", DeployType: "git", GitBuild: db.GitBuildDockerfile}
	checkout(t, c, a.ID, "docker-compose.yml")

	b := c.GitBuildFor(a)
	if !b.Unavailable() {
		t.Error("a Dockerfile build with no Dockerfile in the checkout must report itself unavailable")
	}
	if b.Auto() {
		t.Error("an explicit choice must not report itself as automatic")
	}
	if b.Both() {
		t.Error("Both() with only a compose file in the checkout")
	}
}

// The other deploy types have no checkout, and must not be probed for one —
// an image app whose id happens to collide with a stale directory would
// otherwise change how it is deployed.
func TestGitBuildIgnoresNonGitApps(t *testing.T) {
	c := &Client{appsDir: t.TempDir()}
	a := &db.App{ID: "a1", DeployType: "image", ImageRef: "nginx"}
	checkout(t, c, a.ID, "docker-compose.yml")

	if b := c.GitBuildFor(a); b.Mode != "" || b.HasCompose {
		t.Errorf("GitBuildFor(image app) = %+v, want it empty", b)
	}
	if c.UsesCompose(a) {
		t.Error("an image app must not be run as a compose project")
	}
	if !c.UsesCompose(&db.App{ID: "a2", DeployType: "compose"}) {
		t.Error("a compose app must always be run as a compose project")
	}
}

// docker compose resolves a stack's build contexts and relative paths from
// where the file sits, so a git app's stack has to run from the checkout —
// not from the app directory holding it.
func TestComposeContextRunsFromTheCheckout(t *testing.T) {
	c := &Client{appsDir: t.TempDir()}
	git := &db.App{ID: "a1", DeployType: "git"}
	checkout(t, c, git.ID, "compose.yaml")

	dir, file := c.composeContext(git)
	if dir != c.sourceDir(git.ID) {
		t.Errorf("dir = %q, want the checkout %q", dir, c.sourceDir(git.ID))
	}
	if want := filepath.Join(c.sourceDir(git.ID), "compose.yaml"); file != want {
		t.Errorf("file = %q, want %q", file, want)
	}

	// A pasted compose file is written by Quasar and stays where it put it.
	pasted := &db.App{ID: "a2", DeployType: "compose"}
	dir, file = c.composeContext(pasted)
	if dir != c.AppDir(pasted.ID) || file != filepath.Join(c.AppDir(pasted.ID), "docker-compose.yml") {
		t.Errorf("composeContext(compose app) = %q, %q", dir, file)
	}

	// Nothing to run: callers check for this rather than shelling out to a
	// compose command that would fail with a confusing message.
	if _, file := c.composeContext(&db.App{ID: "a3", DeployType: "git"}); file != "" {
		t.Errorf("file = %q for a checkout with no compose file, want empty", file)
	}
}
