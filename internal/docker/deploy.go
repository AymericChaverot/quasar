package docker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
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

	// readyTimeout bounds how long a new container gets to start serving
	// before the deploy is failed and the previous container kept.
	readyTimeout = 90 * time.Second
	// settleDelay is how long a container with no health path must stay up to
	// count as deployed — enough to catch an image that exits immediately.
	settleDelay = 5 * time.Second
)

var readyClient = &http.Client{Timeout: 5 * time.Second}

// DeployAsync rebuilds the app's containers from what is already on the
// server: the image already pulled, the source already cloned. It is what
// applies a configuration change — new env vars, new limits, new Traefik
// middlewares — without also moving the app onto code nobody has reviewed.
//
// It runs in the background, is recorded in the deployment history and
// notifies on failure. The UI polls Deploying().
func (c *Client) DeployAsync(a *db.App, source string) {
	c.runAsync(a, source, func(ctx context.Context) (string, error) {
		return c.deploy(ctx, a, false)
	})
}

// UpdateAsync fetches the newest version of the app first — a git pull, an
// image pull, a compose pull — and then deploys it. This is the one that
// changes what the app runs.
func (c *Client) UpdateAsync(a *db.App, source string) {
	c.runAsync(a, source, func(ctx context.Context) (string, error) {
		return c.deploy(ctx, a, true)
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

// deploy runs the full deployment for the app's type and returns the image tag
// that ended up running (empty for compose apps). fetch decides whether it
// first goes and gets the newest version from the registry or the repository.
func (c *Client) deploy(ctx context.Context, a *db.App, fetch bool) (string, error) {
	if err := c.EnsureAppDirs(a); err != nil {
		return "", fmt.Errorf("app dirs: %w", err)
	}
	switch a.DeployType {
	case "image":
		return a.ImageRef, c.deployImage(ctx, a, a.ImageRef, fetch)
	case "git":
		tag, err := c.buildFromGit(ctx, a, fetch)
		if err != nil {
			return "", err
		}
		// The image was just built from the local source, so there is nothing
		// to pull for it either way.
		if err := c.deployImage(ctx, a, tag, false); err != nil {
			return tag, err
		}
		c.pruneBuilds(ctx, a.ID)
		return tag, nil
	case "compose":
		return "", c.deployCompose(ctx, a, fetch)
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

// deployImage brings up a new container for the app and retires the one it
// replaces only once the new one is serving. Removing the old container first
// — as this used to — meant every redeploy dropped requests, and a bad image
// or a container that died on startup left the app with no container at all
// and nothing to roll back to.
func (c *Client) deployImage(ctx context.Context, a *db.App, imageRef string, pull bool) error {
	// A deploy that was told not to fetch still cannot run an image that is not
	// there: a first deploy, an image pruned for disk, a restored install. The
	// alternative is the daemon failing later with "No such image".
	if !pull {
		if _, err := c.api.ImageInspect(ctx, imageRef); err != nil {
			pull = true
		}
	}
	if pull {
		rc, err := c.api.ImagePull(ctx, imageRef, image.PullOptions{RegistryAuth: c.registryAuth(imageRef)})
		if err != nil {
			return fmt.Errorf("pull %s: %w", imageRef, err)
		}
		// The daemon accepts the request before it knows whether the pull can
		// succeed and reports a bad tag or a rejected credential inside the
		// stream, so draining it without reading is how a deploy ended up
		// failing later with a confusing "No such image".
		if err := drainPull(rc); err != nil {
			return fmt.Errorf("pull %s: %w", imageRef, err)
		}
	}

	previous := c.appContainers(ctx, a.ID)

	// Two containers writing one data directory at the same time can corrupt
	// it, so an app with a data mount has its old container stopped before the
	// replacement starts: a few seconds of downtime, but the old container is
	// still there to bring back. Only stateless apps truly overlap.
	overlap := a.DataMount == ""
	var stopped []string
	if !overlap {
		for _, ct := range previous {
			if ct.State == "running" {
				stopped = append(stopped, ct.ID)
			}
		}
		for _, id := range stopped {
			c.api.ContainerStop(ctx, id, container.StopOptions{})
		}
	}

	// Cleanup must not inherit a cancelled deploy context, or a timed-out
	// deploy would leave the app down — the exact failure this rewrite exists
	// to prevent.
	rollback := func() {
		undo := context.WithoutCancel(ctx)
		for _, id := range stopped {
			c.api.ContainerStart(undo, id, container.StartOptions{})
		}
	}

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

	name := deployContainerName(a.ID)
	created, err := c.api.ContainerCreate(ctx,
		&container.Config{
			Image:  imageRef,
			Env:    envLines(a.EnvContent),
			Labels: c.traefikLabels(a),
		},
		hostCfg,
		&network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{
				// The alias is how anything on the network keeps addressing
				// the app by one stable name across deploys.
				c.network: {Aliases: []string{ContainerName(a.ID)}},
			},
		},
		nil, name)
	if err != nil {
		rollback()
		return fmt.Errorf("create container: %w", err)
	}
	if err := c.api.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		c.removeContainer(context.WithoutCancel(ctx), created.ID)
		rollback()
		return fmt.Errorf("start container: %w", err)
	}
	if err := c.waitServing(ctx, a, name, created.ID); err != nil {
		c.removeContainer(context.WithoutCancel(ctx), created.ID)
		rollback()
		return err
	}

	// The new container is serving; everything older can go, including any
	// leftovers from an interrupted deploy.
	done := context.WithoutCancel(ctx)
	for _, ct := range previous {
		c.removeContainer(done, ct.ID)
	}
	return nil
}

// waitServing blocks until a freshly started container is actually serving, so
// deployImage knows whether it is safe to retire the container it replaces.
//
// An app with a health path is probed directly by its per-deploy container
// name — not by the shared alias, which would round-robin onto the container
// being replaced and pass even if the new one is broken. Without a health path
// the most we can verify is that it did not exit on startup.
func (c *Client) waitServing(ctx context.Context, a *db.App, name, id string) error {
	probeURL := ""
	if a.HealthPath != "" && a.Port > 0 {
		probeURL = fmt.Sprintf("http://%s:%d%s", name, a.Port, a.HealthPath)
	}

	deadline := time.Now().Add(readyTimeout)
	var lastErr error
	for {
		info, err := c.api.ContainerInspect(ctx, id)
		if err != nil {
			return fmt.Errorf("inspect new container: %w", err)
		}
		if !info.State.Running {
			return fmt.Errorf("new container exited on startup with code %d, previous version kept", info.State.ExitCode)
		}

		if probeURL == "" {
			startedAt, _ := time.Parse(time.RFC3339Nano, info.State.StartedAt)
			if time.Since(startedAt) >= settleDelay {
				return nil
			}
		} else if lastErr = probeOnce(probeURL); lastErr == nil {
			return nil
		}

		if time.Now().After(deadline) {
			if lastErr != nil {
				return fmt.Errorf("new container never served %s within %s (%v), previous version kept",
					a.HealthPath, readyTimeout, lastErr)
			}
			return fmt.Errorf("new container did not stay up for %s, previous version kept", settleDelay)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func probeOnce(url string) error {
	resp, err := readyClient.Get(url)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("status %s", resp.Status)
	}
	return nil
}

// gitURLWithToken injects the platform git token into https clone URLs so
// private repositories work without per-app configuration.
func (c *Client) gitURLWithToken(url string) string {
	// Checked before the lookup: an ssh remote or one that already carries
	// credentials has no use for the token, and no reason to query for it.
	if !strings.HasPrefix(url, "https://") || strings.Contains(url, "@") {
		return url
	}
	token := db.GetSetting(c.dbc, db.SettingGitToken)
	if token == "" {
		return url
	}
	return "https://oauth2:" + token + "@" + strings.TrimPrefix(url, "https://")
}

// syncSource makes apps/<id>/source hold the code to build and returns its
// path: a clone when there is nothing checked out, a fast-forward to the
// branch head when pull is set, and otherwise the commit already on disk.
func (c *Client) syncSource(ctx context.Context, a *db.App, pull bool) (string, error) {
	src := filepath.Join(c.AppDir(a.ID), "source")

	var cmd *exec.Cmd
	switch _, err := os.Stat(filepath.Join(src, ".git")); {
	case err != nil:
		// No checkout to build from, so this clones whether or not an update
		// was asked for.
		os.RemoveAll(src)
		args := []string{"clone", "--depth", "1"}
		if a.GitBranch != "" {
			args = append(args, "--branch", a.GitBranch)
		}
		args = append(args, c.gitURLWithToken(a.GitURL), src)
		cmd = exec.CommandContext(ctx, "git", args...)
	case pull:
		cmd = exec.CommandContext(ctx, "git", "-C", src, "pull", "--ff-only")
	default:
		return src, nil // rebuild the commit already there
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("git: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return src, nil
}

// buildFromGit clones (or updates) the repo into apps/<id>/source and builds
// a timestamped image from its Dockerfile, so past builds stay available for
// rollback until pruneBuilds trims them.
//
// pull decides whether the existing checkout is advanced to the branch head
// first. Without it the image is rebuilt from the commit already on disk,
// which is what makes a redeploy repeatable: the same code, with whatever
// configuration changed around it.
func (c *Client) buildFromGit(ctx context.Context, a *db.App, pull bool) (string, error) {
	src, err := c.syncSource(ctx, a, pull)
	if err != nil {
		return "", err
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
//
// `up` only pulls an image it does not already have, so a compose app pinned
// to a moving tag would otherwise keep running the copy it was first deployed
// with, forever. pull is what makes it advance.
func (c *Client) deployCompose(ctx context.Context, a *db.App, pull bool) error {
	dir := c.AppDir(a.ID)
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte(a.ComposeYAML), 0o644); err != nil {
		return err
	}
	if pull {
		// --ignore-buildable: a service with a build context has no image to
		// pull, and its absence from the registry is not an error.
		if err := c.compose(ctx, a.ID, "pull", "--ignore-buildable"); err != nil {
			return err
		}
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
