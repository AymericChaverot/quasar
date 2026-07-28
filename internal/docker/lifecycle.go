package docker

import (
	"context"
	"errors"
	"os"
	"sort"
	"strconv"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"

	"quasar/internal/db"
)

// AppStatus is the live state shown in the UI.
type AppStatus struct {
	State  string // "running", "stopped", "error", "deploying", "not deployed"
	Uptime string // human duration, empty unless running
}

// ErrNoContainer is returned when an app has never been deployed, is stopped
// and removed, or had its container removed outside Quasar. Callers that must
// distinguish "nothing to do" from a real failure check for it — a backup skips
// dumping an app that isn't running rather than failing the whole run.
var ErrNoContainer = errors.New("app has no container")

// appContainers lists an app's managed containers, newest first. A deploy
// keeps the previous container running until the replacement is serving, so
// two can exist at once; everything that acts on "the" container acts on the
// newest, and only the deploy itself looks at the rest.
//
// Resolution goes through the label rather than the container name because
// names carry a per-deploy suffix. Containers created before that suffix
// existed are named qs-<id> but carry the same label, so they resolve too.
func (c *Client) appContainers(ctx context.Context, appID string) []container.Summary {
	list, err := c.api.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: filters.NewArgs(filters.Arg("label", appLabel+"="+appID)),
	})
	if err != nil {
		return nil
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Created > list[j].Created })
	return list
}

// appContainer resolves the container currently serving an app.
func (c *Client) appContainer(ctx context.Context, appID string) (string, error) {
	list := c.appContainers(ctx, appID)
	if len(list) == 0 {
		return "", ErrNoContainer
	}
	return list[0].ID, nil
}

// composeContainers lists every container of a compose app's project.
func (c *Client) composeContainers(ctx context.Context, appID string) []container.Summary {
	list, err := c.api.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: filters.NewArgs(filters.Arg("label", "com.docker.compose.project="+composeProject(appID))),
	})
	if err != nil {
		return nil
	}
	return list
}

// composeWebContainer picks the container a compose app serves HTTP from: the
// one its author put the Traefik labels on. Quasar does not write those files,
// so that label is the only reliable marker of which service is the front end.
func (c *Client) composeWebContainer(ctx context.Context, appID string) (container.Summary, bool) {
	for _, ct := range c.composeContainers(ctx, appID) {
		if ct.Labels["traefik.enable"] == "true" {
			return ct, true
		}
	}
	return container.Summary{}, false
}

// Status inspects the app's container(s) and reports a UI-friendly state.
func (c *Client) Status(ctx context.Context, a *db.App) AppStatus {
	if d := c.Deploying(a.ID); d != nil {
		if d.Running {
			return AppStatus{State: "deploying"}
		}
		if d.Err != "" {
			return AppStatus{State: "error"}
		}
	}

	if c.UsesCompose(a) {
		return c.composeStatus(ctx, a.ID)
	}

	id, err := c.appContainer(ctx, a.ID)
	if err != nil {
		return AppStatus{State: "not deployed"}
	}
	info, err := c.api.ContainerInspect(ctx, id)
	if err != nil {
		return AppStatus{State: "not deployed"}
	}
	return statusFromState(info.State.Status, info.State.StartedAt, info.State.ExitCode)
}

func (c *Client) composeStatus(ctx context.Context, appID string) AppStatus {
	list := c.composeContainers(ctx, appID)
	if len(list) == 0 {
		return AppStatus{State: "not deployed"}
	}
	running := 0
	for _, ct := range list {
		if ct.State == "running" {
			running++
		}
	}
	switch {
	case running == len(list):
		return AppStatus{State: "running", Uptime: humanDuration(time.Since(time.Unix(list[0].Created, 0)))}
	case running == 0:
		return AppStatus{State: "stopped"}
	default:
		return AppStatus{State: "error"} // partially up
	}
}

func statusFromState(state, startedAt string, exitCode int) AppStatus {
	switch state {
	case "running":
		up := ""
		if t, err := time.Parse(time.RFC3339Nano, startedAt); err == nil {
			up = humanDuration(time.Since(t))
		}
		return AppStatus{State: "running", Uptime: up}
	case "exited":
		if exitCode != 0 {
			return AppStatus{State: "error"}
		}
		return AppStatus{State: "stopped"}
	case "created", "paused":
		return AppStatus{State: "stopped"}
	default:
		return AppStatus{State: state}
	}
}

func (c *Client) Start(ctx context.Context, a *db.App) error {
	if c.UsesCompose(a) {
		return c.compose(ctx, a, "start")
	}
	id, err := c.appContainer(ctx, a.ID)
	if err != nil {
		return err
	}
	return c.api.ContainerStart(ctx, id, container.StartOptions{})
}

func (c *Client) Stop(ctx context.Context, a *db.App) error {
	if c.UsesCompose(a) {
		return c.compose(ctx, a, "stop")
	}
	id, err := c.appContainer(ctx, a.ID)
	if err != nil {
		return err
	}
	return c.api.ContainerStop(ctx, id, container.StopOptions{})
}

func (c *Client) Restart(ctx context.Context, a *db.App) error {
	if c.UsesCompose(a) {
		return c.compose(ctx, a, "restart")
	}
	id, err := c.appContainer(ctx, a.ID)
	if err != nil {
		return err
	}
	return c.api.ContainerRestart(ctx, id, container.StopOptions{})
}

// Remove tears down the app's containers and deletes its directory on disk.
func (c *Client) Remove(ctx context.Context, a *db.App) error {
	if c.UsesCompose(a) {
		c.compose(ctx, a, "down", "--volumes") // best effort
	}
	// Both sets, whichever way the app is deployed today: an interrupted deploy
	// can leave a replacement container behind, and a git app built the other
	// way before this one keeps its containers somewhere else entirely.
	for _, ct := range c.appContainers(ctx, a.ID) {
		c.removeContainer(ctx, ct.ID)
	}
	for _, ct := range c.composeContainers(ctx, a.ID) {
		c.removeContainer(ctx, ct.ID)
	}
	c.forgetDeploy(a.ID)
	return os.RemoveAll(c.AppDir(a.ID))
}

func (c *Client) removeContainer(ctx context.Context, name string) {
	c.api.ContainerRemove(ctx, name, container.RemoveOptions{Force: true})
}

func humanDuration(d time.Duration) string {
	i := strconv.Itoa
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return i(int(d.Minutes())) + "m"
	case d < 24*time.Hour:
		return i(int(d.Hours())) + "h " + i(int(d.Minutes())%60) + "m"
	default:
		return i(int(d.Hours()/24)) + "d " + i(int(d.Hours())%24) + "h"
	}
}
