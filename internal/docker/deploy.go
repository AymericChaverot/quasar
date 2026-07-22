package docker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/registry"
	"github.com/moby/go-archive"

	"quasar/internal/db"
	"quasar/internal/notify"
)

const (
	deployTimeout = 15 * time.Minute
	keepBuilds    = 4 // git-built images kept per app for rollback
)

// DeployAsync launches a deploy in the background, records it in the
// deployment history and notifies on failure. The UI polls Deploying().
func (c *Client) DeployAsync(a *db.App, source string) {
	c.runAsync(a, source, func(ctx context.Context) (string, error) {
		return c.deploy(ctx, a)
	})
}

// RollbackAsync redeploys a previously used image tag.
func (c *Client) RollbackAsync(a *db.App, tag string) {
	c.runAsync(a, "rollback", func(ctx context.Context) (string, error) {
		if err := c.EnsureAppDirs(a); err != nil {
			return tag, err
		}
		// Local build tags still exist on the daemon; registry refs need a pull.
		pull := !strings.HasPrefix(tag, buildTagPrefix(a.ID)+":")
		return tag, c.deployImage(ctx, a, tag, pull)
	})
}

func (c *Client) runAsync(a *db.App, source string, fn func(ctx context.Context) (string, error)) {
	depID := db.StartDeployment(c.dbc, a.ID, source)
	c.setDeploy(a.ID, true, "")
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), deployTimeout)
		defer cancel()
		tag, err := fn(ctx)
		if err != nil {
			log.Printf("deploy %s (%s): %v", a.Name, a.ID, err)
			db.FinishDeployment(c.dbc, depID, "failed", err.Error(), tag)
			notify.Send(c.dbc, fmt.Sprintf("Quasar: deploy failed for %s (%s): %v", a.Name, a.Subdomain, err))
			c.setDeploy(a.ID, false, err.Error())
			return
		}
		db.FinishDeployment(c.dbc, depID, "success", "", tag)
		c.setDeploy(a.ID, false, "")
	}()
}

// deploy runs the full deployment for the app's type and returns the image
// tag that ended up running (empty for compose apps).
func (c *Client) deploy(ctx context.Context, a *db.App) (string, error) {
	if err := c.EnsureAppDirs(a); err != nil {
		return "", fmt.Errorf("app dirs: %w", err)
	}
	switch a.DeployType {
	case "image":
		return a.ImageRef, c.deployImage(ctx, a, a.ImageRef, true)
	case "git":
		tag, err := c.buildFromGit(ctx, a)
		if err != nil {
			return "", err
		}
		if err := c.deployImage(ctx, a, tag, false); err != nil {
			return tag, err
		}
		c.pruneBuilds(ctx, a.ID)
		return tag, nil
	case "compose":
		return "", c.deployCompose(ctx, a)
	default:
		return "", fmt.Errorf("unknown deploy type %q", a.DeployType)
	}
}

// registryAuth returns the base64 auth header for a reference's registry,
// or "" when no credentials are stored for it.
func (c *Client) registryAuth(ref string) string {
	reg := db.RegistryFor(c.dbc, refRegistry(ref))
	if reg == nil {
		return ""
	}
	buf, _ := json.Marshal(registry.AuthConfig{
		Username:      reg.Username,
		Password:      reg.Secret,
		ServerAddress: reg.Server,
	})
	return base64.URLEncoding.EncodeToString(buf)
}

// deployImage replaces the app container with a fresh one from the given image.
func (c *Client) deployImage(ctx context.Context, a *db.App, imageRef string, pull bool) error {
	if pull {
		rc, err := c.api.ImagePull(ctx, imageRef, image.PullOptions{RegistryAuth: c.registryAuth(imageRef)})
		if err != nil {
			return fmt.Errorf("pull %s: %w", imageRef, err)
		}
		io.Copy(io.Discard, rc) // wait for the pull to finish
		rc.Close()
	}

	name := ContainerName(a.ID)
	c.removeContainer(ctx, name)

	hostCfg := &container.HostConfig{
		RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyUnlessStopped},
		Resources: container.Resources{
			NanoCPUs: int64(a.CPULimit * 1e9),
			Memory:   a.MemLimitMB << 20,
		},
	}
	if a.DataMount != "" {
		hostCfg.Binds = []string{c.hostDataDir(a.ID) + ":" + a.DataMount}
	}

	created, err := c.api.ContainerCreate(ctx,
		&container.Config{
			Image:  imageRef,
			Env:    envLines(a.EnvContent),
			Labels: c.traefikLabels(a),
		},
		hostCfg,
		&network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{c.network: {}},
		},
		nil, name)
	if err != nil {
		return fmt.Errorf("create container: %w", err)
	}
	if err := c.api.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		return fmt.Errorf("start container: %w", err)
	}
	return nil
}

// gitURLWithToken injects the platform git token into https clone URLs so
// private repositories work without per-app configuration.
func (c *Client) gitURLWithToken(url string) string {
	token := db.GetSetting(c.dbc, db.SettingGitToken)
	if token == "" || !strings.HasPrefix(url, "https://") || strings.Contains(url, "@") {
		return url
	}
	return "https://oauth2:" + token + "@" + strings.TrimPrefix(url, "https://")
}

// buildFromGit clones (or updates) the repo into apps/<id>/source and builds
// a timestamped image from its Dockerfile, so past builds stay available for
// rollback until pruneBuilds trims them.
func (c *Client) buildFromGit(ctx context.Context, a *db.App) (string, error) {
	src := filepath.Join(c.AppDir(a.ID), "source")

	var cmd *exec.Cmd
	if _, err := os.Stat(filepath.Join(src, ".git")); err == nil {
		cmd = exec.CommandContext(ctx, "git", "-C", src, "pull", "--ff-only")
	} else {
		os.RemoveAll(src)
		args := []string{"clone", "--depth", "1"}
		if a.GitBranch != "" {
			args = append(args, "--branch", a.GitBranch)
		}
		args = append(args, c.gitURLWithToken(a.GitURL), src)
		cmd = exec.CommandContext(ctx, "git", args...)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("git: %s: %w", strings.TrimSpace(string(out)), err)
	}

	buildCtx, err := archive.TarWithOptions(src, &archive.TarOptions{})
	if err != nil {
		return "", fmt.Errorf("tar build context: %w", err)
	}
	defer buildCtx.Close()

	tag := fmt.Sprintf("%s:%d", buildTagPrefix(a.ID), time.Now().Unix())
	resp, err := c.api.ImageBuild(ctx, buildCtx, types.ImageBuildOptions{
		Tags:       []string{tag},
		Dockerfile: "Dockerfile",
		Remove:     true,
	})
	if err != nil {
		return "", fmt.Errorf("build: %w", err)
	}
	defer resp.Body.Close()
	// The build only actually runs while the response stream is consumed;
	// scan it for an error message as we drain.
	if err := drainBuildOutput(resp.Body); err != nil {
		return "", err
	}
	return tag, nil
}

// pruneBuilds removes old git-built images beyond the rollback window.
func (c *Client) pruneBuilds(ctx context.Context, appID string) {
	list, err := c.api.ImageList(ctx, image.ListOptions{
		Filters: filters.NewArgs(filters.Arg("reference", buildTagPrefix(appID)+":*")),
	})
	if err != nil || len(list) <= keepBuilds {
		return
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Created > list[j].Created })
	for _, img := range list[keepBuilds:] {
		c.api.ImageRemove(ctx, img.ID, image.RemoveOptions{Force: true, PruneChildren: true})
	}
}

// BuildTags lists the app's locally available git-build tags (for rollback).
func (c *Client) BuildTags(ctx context.Context, appID string) []string {
	list, err := c.api.ImageList(ctx, image.ListOptions{
		Filters: filters.NewArgs(filters.Arg("reference", buildTagPrefix(appID)+":*")),
	})
	if err != nil {
		return nil
	}
	var tags []string
	for _, img := range list {
		tags = append(tags, img.RepoTags...)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(tags)))
	return tags
}

// deployCompose writes the compose file and shells out to the docker compose
// CLI (installed in the dashboard image, pointed at the socket proxy).
func (c *Client) deployCompose(ctx context.Context, a *db.App) error {
	dir := c.AppDir(a.ID)
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte(a.ComposeYAML), 0o644); err != nil {
		return err
	}
	return c.compose(ctx, a.ID, "up", "-d", "--build", "--remove-orphans")
}

// compose runs `docker compose` for an app with its project name and files.
func (c *Client) compose(ctx context.Context, appID string, args ...string) error {
	dir := c.AppDir(appID)
	base := []string{"compose", "-p", composeProject(appID), "-f", filepath.Join(dir, "docker-compose.yml")}
	if _, err := os.Stat(filepath.Join(dir, ".env")); err == nil {
		base = append(base, "--env-file", filepath.Join(dir, ".env"))
	}
	cmd := exec.CommandContext(ctx, "docker", append(base, args...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("docker compose %s: %s: %w", args[0], strings.TrimSpace(string(out)), err)
	}
	return nil
}

// envLines splits .env-style content into KEY=VALUE strings for the container.
func envLines(content string) []string {
	var env []string
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || !strings.Contains(line, "=") {
			continue
		}
		env = append(env, line)
	}
	return env
}
