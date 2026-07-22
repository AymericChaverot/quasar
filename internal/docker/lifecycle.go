package docker

import (
	"context"
	"os"
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

	if a.DeployType == "compose" {
		return c.composeStatus(ctx, a.ID)
	}

	info, err := c.api.ContainerInspect(ctx, ContainerName(a.ID))
	if err != nil {
		return AppStatus{State: "not deployed"}
	}
	return statusFromState(info.State.Status, info.State.StartedAt, info.State.ExitCode)
}

func (c *Client) composeStatus(ctx context.Context, appID string) AppStatus {
	list, err := c.api.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: filters.NewArgs(filters.Arg("label", "com.docker.compose.project="+composeProject(appID))),
	})
	if err != nil || len(list) == 0 {
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
	if a.DeployType == "compose" {
		return c.compose(ctx, a.ID, "start")
	}
	return c.api.ContainerStart(ctx, ContainerName(a.ID), container.StartOptions{})
}

func (c *Client) Stop(ctx context.Context, a *db.App) error {
	if a.DeployType == "compose" {
		return c.compose(ctx, a.ID, "stop")
	}
	return c.api.ContainerStop(ctx, ContainerName(a.ID), container.StopOptions{})
}

func (c *Client) Restart(ctx context.Context, a *db.App) error {
	if a.DeployType == "compose" {
		return c.compose(ctx, a.ID, "restart")
	}
	return c.api.ContainerRestart(ctx, ContainerName(a.ID), container.StopOptions{})
}

// Remove tears down the app's containers and deletes its directory on disk.
func (c *Client) Remove(ctx context.Context, a *db.App) error {
	if a.DeployType == "compose" {
		c.compose(ctx, a.ID, "down", "--volumes") // best effort
	} else {
		c.removeContainer(ctx, ContainerName(a.ID))
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
