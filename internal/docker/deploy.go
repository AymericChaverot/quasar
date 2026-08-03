package docker

import (
	"bytes"
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
		return c.deployGit(ctx, a, fetch)
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
		if err := drainPull(rc, nil); err != nil {
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

// syncSource makes apps/<id>/source hold the code to build and returns its
// path: a clone when there is nothing checked out, a fast-forward to the
// branch head when pull is set, and otherwise the commit already on disk.
//
// The clone URL stays exactly as the operator entered it. Credentials reach
// git through the environment instead (see gitRun), which is what keeps the
// checkout on disk free of secrets and lets a rotated token take effect
// without re-cloning.
func (c *Client) syncSource(ctx context.Context, a *db.App, pull bool) (string, error) {
	src := c.sourceDir(a.ID)

	var args []string
	switch _, err := os.Stat(filepath.Join(src, ".git")); {
	case err != nil:
		// No checkout to build from, so this clones whether or not an update
		// was asked for.
		os.RemoveAll(src)
		args = []string{"clone", "--depth", "1"}
		if a.GitBranch != "" {
			args = append(args, "--branch", a.GitBranch)
		}
		args = append(args, a.GitURL, src)
	case pull:
		args = []string{"-C", src, "pull", "--ff-only"}
	default:
		return src, nil // rebuild the commit already there
	}
	if err := c.gitRun(ctx, a.GitURL, args...); err != nil {
		return "", err
	}
	c.dropStoredCredential(ctx, src, a.GitURL)
	return src, nil
}

// dropStoredCredential rewrites a remote that carries credentials back to the
// bare URL.
//
// Nothing here writes such a remote any more, but installs created before this
// have one: earlier versions cloned from a URL with the token embedded, and
// git copied it verbatim into .git/config. That leaves the token inside the
// app's source directory — and inside every backup of it — and pins the
// checkout to that one token, so rotating the credential would change nothing.
func (c *Client) dropStoredCredential(ctx context.Context, src, plainURL string) {
	if !strings.HasPrefix(plainURL, "https://") {
		return
	}
	out, err := exec.CommandContext(ctx, "git", "-C", src, "remote", "get-url", "origin").Output()
	if err != nil || !strings.Contains(strings.TrimSpace(string(out)), "@") {
		return
	}
	exec.CommandContext(ctx, "git", "-C", src, "remote", "set-url", "origin", plainURL).Run()
}

// deployGit clones (or updates) the repository and then runs it the way the
// repository itself asks to be run: as a compose stack when it carries a
// compose file, otherwise as one container built from its Dockerfile. See
// GitBuildFor for how that is decided and how an operator overrides it.
//
// pull decides whether the existing checkout is advanced to the branch head
// first. Without it the app is redeployed from the commit already on disk,
// which is what makes a redeploy repeatable: the same code, with whatever
// configuration changed around it.
//
// The returned tag is empty for a stack, which has no single image to name.
func (c *Client) deployGit(ctx context.Context, a *db.App, pull bool) (string, error) {
	src, err := c.syncSource(ctx, a, pull)
	if err != nil {
		return "", err
	}

	// Switching a repository between the two modes moves its containers
	// somewhere else entirely, so whatever the app ran before has to be
	// removed here — nothing further down would recognise it as this app's.
	// Volumes are left alone: a mode switch is not a request to lose data.
	switch c.GitBuildFor(a).Mode {
	case db.GitBuildCompose:
		for _, ct := range c.appContainers(ctx, a.ID) {
			c.removeContainer(ctx, ct.ID)
		}
		if err := c.writeSourceEnv(ctx, src, a); err != nil {
			return "", err
		}
		return "", c.composeUp(ctx, a, pull)

	case db.GitBuildDockerfile:
		for _, ct := range c.composeContainers(ctx, a.ID) {
			c.removeContainer(ctx, ct.ID)
		}
		tag, err := c.buildImage(ctx, a, src)
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

	default:
		return "", fmt.Errorf("the repository has neither a Dockerfile nor a compose file at its root")
	}
}

// writeSourceEnv puts the app's environment where a repository's own compose
// file expects to find it: a .env beside the compose file.
//
// Quasar keeps the canonical copy in apps/<id>/.env, outside the checkout, and
// passes it as --env-file — but that only feeds ${VAR} interpolation of the
// compose file itself. What actually puts variables inside a container is the
// service's own `env_file: .env`, and that resolves against the project
// directory, which for a git app is the checkout. Without this the environment
// an operator typed into the dashboard would reach nothing.
//
// A .env the repository tracks is left alone: overwriting it would discard
// something the author committed on purpose, and leave a dirty working tree
// that the next `git pull --ff-only` refuses to advance.
func (c *Client) writeSourceEnv(ctx context.Context, src string, a *db.App) error {
	if gitTracks(ctx, src, ".env") {
		return nil
	}
	return os.WriteFile(filepath.Join(src, ".env"), []byte(a.EnvContent+"\n"), 0o600)
}

// gitTracks reports whether a checkout has the given path under version
// control (as opposed to it being absent, or present but untracked).
func gitTracks(ctx context.Context, src, path string) bool {
	cmd := exec.CommandContext(ctx, "git", "-C", src, "ls-files", "--error-unmatch", "--", path)
	return cmd.Run() == nil
}

// buildImage builds a timestamped image from the checkout's Dockerfile, so
// past builds stay available for rollback until pruneBuilds trims them.
func (c *Client) buildImage(ctx context.Context, a *db.App, src string) (string, error) {
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

// deployCompose writes the compose file the operator pasted into Quasar and
// brings its stack up. A git app skips the writing — its compose file comes
// from the repository — and goes straight to composeUp.
func (c *Client) deployCompose(ctx context.Context, a *db.App, pull bool) error {
	dir := c.AppDir(a.ID)
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte(a.ComposeYAML), 0o644); err != nil {
		return err
	}
	return c.composeUp(ctx, a, pull)
}

// composeUp brings an app's stack up through the docker compose CLI (installed
// in the dashboard image, pointed at the socket proxy).
//
// `up` only pulls an image it does not already have, so a stack pinned to a
// moving tag would otherwise keep running the copy it was first deployed with,
// forever. pull is what makes it advance.
func (c *Client) composeUp(ctx context.Context, a *db.App, pull bool) error {
	// First, because it decides which file everything below runs with: an
	// ordinary compose file is rewritten here into one that can run behind
	// Traefik.
	if err := c.writeAdaptedCompose(a); err != nil {
		return err
	}
	// Then, for what the rewrite could not settle — a stack whose front end
	// Quasar could not single out still publishes port 80. A collision fails on
	// the very last container compose starts, having spent the whole deploy
	// building images and starting the other services.
	if err := c.checkComposePorts(ctx, a); err != nil {
		return err
	}
	if pull {
		// --ignore-buildable: a service with a build context has no image to
		// pull, and its absence from the registry is not an error.
		if err := c.compose(ctx, a, "pull", "--ignore-buildable"); err != nil {
			return err
		}
	}
	return c.compose(ctx, a, "up", "-d", "--build", "--remove-orphans")
}

// composeCmd builds a `docker compose` invocation for an app, with its project
// name and files.
func (c *Client) composeCmd(ctx context.Context, a *db.App, args ...string) (*exec.Cmd, error) {
	dir, file := c.composeContext(a)
	if file == "" {
		return nil, fmt.Errorf("this application has no compose file")
	}
	base := []string{"compose", "-p", composeProject(a.ID), "-f", file}
	// The app's env lives where Quasar put it, which for a git app is beside
	// the checkout rather than in it.
	env := filepath.Join(c.AppDir(a.ID), ".env")
	if _, err := os.Stat(env); err == nil {
		base = append(base, "--env-file", env)
	}
	cmd := exec.CommandContext(ctx, "docker", append(base, args...)...)
	cmd.Dir = dir
	return cmd, nil
}

// compose runs `docker compose` for an app.
func (c *Client) compose(ctx context.Context, a *db.App, args ...string) error {
	cmd, err := c.composeCmd(ctx, a, args...)
	if err != nil {
		return err
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("docker compose %s: %s: %w", args[0], composeTail(out), err)
	}
	return nil
}

// composeOutput runs `docker compose` and returns its stdout alone, keeping
// stderr for the error message — compose writes warnings there, which would
// otherwise land in the middle of the JSON a caller is parsing.
func (c *Client) composeOutput(ctx context.Context, a *db.App, args ...string) ([]byte, error) {
	cmd, err := c.composeCmd(ctx, a, args...)
	if err != nil {
		return nil, err
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("docker compose %s: %s: %w", args[0], composeTail(stderr.Bytes()), err)
	}
	return stdout.Bytes(), nil
}

// composeErrLines is how much of a failed compose command's output is quoted
// back to the operator.
const composeErrLines = 20

// composeTail keeps the end of a compose command's output for the error
// message. Building a stack streams hundreds of BuildKit progress lines and
// only says what actually went wrong on the last few, so quoting all of it
// buries the failure — in the status panel, which renders it as one block, and
// in the deployments table, which stores it.
func composeTail(out []byte) string {
	var kept []string
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimRight(line, " \t\r"); line != "" {
			kept = append(kept, line)
		}
	}
	if len(kept) > composeErrLines {
		omitted := fmt.Sprintf("[%d earlier lines omitted]", len(kept)-composeErrLines)
		kept = append([]string{omitted}, kept[len(kept)-composeErrLines:]...)
	}
	return strings.Join(kept, "\n")
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
