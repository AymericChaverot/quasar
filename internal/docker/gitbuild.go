package docker

import (
	"os"
	"path/filepath"

	"quasar/internal/db"
)

// composeFilenames are the names docker compose itself looks for at the root of
// a project, in the order it prefers them.
var composeFilenames = []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"}

// GitBuild is how a git app's checkout is built, next to what the checkout
// actually offers. Both matter to the admin page: the choice only means
// anything once the repository is on disk, and until then there is nothing to
// choose between.
type GitBuild struct {
	Mode          string // "compose", "dockerfile", or "" with nothing checked out
	Choice        string // what the app is configured with (db.GitBuild*)
	HasCompose    bool
	HasDockerfile bool
	ComposeFile   string // name of the compose file found in the checkout
}

// Auto reports whether the mode came from what the repository holds rather
// than from an explicit choice.
func (b GitBuild) Auto() bool { return b.Choice == db.GitBuildAuto }

// Unavailable reports a choice the checkout cannot honour — a repository whose
// Dockerfile was deleted, say — which is why the app builds the other way.
func (b GitBuild) Unavailable() bool {
	return b.Mode != "" && b.Choice != db.GitBuildAuto && b.Choice != b.Mode
}

// Both reports a repository that offers either way of building, which is the
// only case where the choice changes anything.
func (b GitBuild) Both() bool { return b.HasCompose && b.HasDockerfile }

// sourceDir is where a git app's checkout lives.
func (c *Client) sourceDir(appID string) string {
	return filepath.Join(c.AppDir(appID), "source")
}

// findComposeFile returns the name of the compose file at the root of dir, or
// "" when it has none.
func findComposeFile(dir string) string {
	for _, name := range composeFilenames {
		if st, err := os.Stat(filepath.Join(dir, name)); err == nil && !st.IsDir() {
			return name
		}
	}
	return ""
}

// GitBuildFor reports how an app's checkout is built. A repository carrying a
// compose file is deployed as a stack: a Dockerfile beside it describes one of
// the stack's services, so building that alone would run a fraction of the app
// and call it deployed. An explicit choice overrides that when the checkout can
// honour it — a repository with no Dockerfile cannot be built from one.
func (c *Client) GitBuildFor(a *db.App) GitBuild {
	b := GitBuild{Choice: a.GitBuild}
	if a.DeployType != "git" {
		return b
	}
	src := c.sourceDir(a.ID)
	b.ComposeFile = findComposeFile(src)
	b.HasCompose = b.ComposeFile != ""
	if st, err := os.Stat(filepath.Join(src, "Dockerfile")); err == nil && !st.IsDir() {
		b.HasDockerfile = true
	}
	switch {
	case b.HasCompose && a.GitBuild != db.GitBuildDockerfile:
		b.Mode = db.GitBuildCompose
	case b.HasDockerfile:
		b.Mode = db.GitBuildDockerfile
	case b.HasCompose:
		b.Mode = db.GitBuildCompose // asked for the Dockerfile the repo does not have
	}
	return b
}

// UsesCompose reports whether the app runs as a docker compose project rather
// than as a single container Quasar creates itself. It decides more than the
// deploy does: which containers count as the app's, where a shell lands, what a
// restart restarts, and whether Quasar's own Traefik labels are involved at all.
func (c *Client) UsesCompose(a *db.App) bool {
	if a.DeployType == "compose" {
		return true
	}
	return a.DeployType == "git" && c.GitBuildFor(a).Mode == db.GitBuildCompose
}

// composeContext returns the directory docker compose runs in and the file it
// runs with. A git app's stack is driven from its checkout, since a compose
// file's build contexts and relative paths resolve from where the file sits.
// The file is "" when the app has none, which is what compose callers check.
func (c *Client) composeContext(a *db.App) (dir, file string) {
	if a.DeployType == "compose" {
		dir = c.AppDir(a.ID)
		return dir, filepath.Join(dir, "docker-compose.yml")
	}
	src := c.sourceDir(a.ID)
	name := findComposeFile(src)
	if name == "" {
		return src, ""
	}
	return src, filepath.Join(src, name)
}
