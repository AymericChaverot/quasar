package docker

import (
	"bytes"
	"context"
	"fmt"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"

	"quasar/internal/db"
)

// containerFor resolves the container name/ID to target for an app: the
// managed container, or the first container of a compose project.
func (c *Client) containerFor(ctx context.Context, a *db.App) (string, error) {
	if a.DeployType != "compose" {
		return ContainerName(a.ID), nil
	}
	list, err := c.api.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return "", err
	}
	for _, ct := range list {
		if ct.Labels["com.docker.compose.project"] == composeProject(a.ID) {
			return ct.ID, nil
		}
	}
	return "", fmt.Errorf("no container found for this compose project")
}

// RunCommand executes a shell command inside the app's container and returns
// its combined output; err is non-nil when the command exits non-zero.
func (c *Client) RunCommand(ctx context.Context, a *db.App, command string) (string, error) {
	target, err := c.containerFor(ctx, a)
	if err != nil {
		return "", err
	}
	exec, err := c.api.ContainerExecCreate(ctx, target, container.ExecOptions{
		Cmd:          []string{"/bin/sh", "-c", command},
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return "", fmt.Errorf("exec create: %w", err)
	}
	resp, err := c.api.ContainerExecAttach(ctx, exec.ID, container.ExecStartOptions{})
	if err != nil {
		return "", fmt.Errorf("exec attach: %w", err)
	}
	defer resp.Close()

	var buf bytes.Buffer
	stdcopy.StdCopy(&buf, &buf, resp.Reader)

	inspect, err := c.api.ContainerExecInspect(ctx, exec.ID)
	if err != nil {
		return buf.String(), err
	}
	if inspect.ExitCode != 0 {
		return buf.String(), fmt.Errorf("exit code %d", inspect.ExitCode)
	}
	return buf.String(), nil
}

// InteractiveShell opens a TTY /bin/sh inside the app's container and returns
// the bidirectional stream plus the exec ID (needed for resizes).
func (c *Client) InteractiveShell(ctx context.Context, a *db.App) (types.HijackedResponse, string, error) {
	target, err := c.containerFor(ctx, a)
	if err != nil {
		return types.HijackedResponse{}, "", err
	}
	exec, err := c.api.ContainerExecCreate(ctx, target, container.ExecOptions{
		Cmd:          []string{"/bin/sh"},
		Tty:          true,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		Env:          []string{"TERM=xterm-256color"},
	})
	if err != nil {
		return types.HijackedResponse{}, "", fmt.Errorf("exec create: %w", err)
	}
	resp, err := c.api.ContainerExecAttach(ctx, exec.ID, container.ExecStartOptions{Tty: true})
	if err != nil {
		return types.HijackedResponse{}, "", fmt.Errorf("exec attach: %w", err)
	}
	return resp, exec.ID, nil
}

func (c *Client) ResizeShell(ctx context.Context, execID string, rows, cols uint) {
	c.api.ContainerExecResize(ctx, execID, container.ResizeOptions{Height: rows, Width: cols})
}

// HealthURL is the in-network URL probed by the health checker (the dashboard
// shares the traefik network with app containers).
func (c *Client) HealthURL(a *db.App) string {
	return fmt.Sprintf("http://%s:%d%s", ContainerName(a.ID), a.Port, a.HealthPath)
}
