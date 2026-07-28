package docker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"

	"quasar/internal/db"
)

// dumpTimeout bounds a pre-backup command. Generous, because dumping a large
// database is slow, but bounded so a hung command cannot stall backups forever.
const dumpTimeout = 30 * time.Minute

// containerFor resolves the container ID to target for an app: the container
// currently serving it, or the first container of a compose project.
func (c *Client) containerFor(ctx context.Context, a *db.App) (string, error) {
	if !c.UsesCompose(a) {
		return c.appContainer(ctx, a.ID)
	}
	// The web container when its author labelled one, so a shell or a log
	// stream lands on the front end rather than on whichever service the
	// daemon happens to list first.
	if web, ok := c.composeWebContainer(ctx, a.ID); ok {
		return web.ID, nil
	}
	if list := c.composeContainers(ctx, a.ID); len(list) > 0 {
		return list[0].ID, nil
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

// CaptureCommand runs a command in the app's container and returns its stdout
// alone, keeping stderr for the error message.
//
// RunCommand merges the two, which is right for a task's log but ruins a dump:
// pg_dump and mysqldump write progress and warnings to stderr, and mixing that
// into the SQL produces an archive that will not reload.
func (c *Client) CaptureCommand(ctx context.Context, a *db.App, command string) ([]byte, error) {
	target, err := c.containerFor(ctx, a)
	if err != nil {
		return nil, err
	}
	exec, err := c.api.ContainerExecCreate(ctx, target, container.ExecOptions{
		Cmd:          []string{"/bin/sh", "-c", command},
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return nil, fmt.Errorf("exec create: %w", err)
	}
	resp, err := c.api.ContainerExecAttach(ctx, exec.ID, container.ExecStartOptions{})
	if err != nil {
		return nil, fmt.Errorf("exec attach: %w", err)
	}
	defer resp.Close()

	var stdout, stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, &stderr, resp.Reader); err != nil {
		return nil, fmt.Errorf("read command output: %w", err)
	}

	inspect, err := c.api.ContainerExecInspect(ctx, exec.ID)
	if err != nil {
		return nil, err
	}
	if inspect.ExitCode != 0 {
		return nil, fmt.Errorf("exit code %d: %s", inspect.ExitCode, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// DumpForBackup runs an app's pre-backup command, returning nil output when the
// app has no running container. A stopped database is not writing, so its data
// directory is already consistent in the archive and there is nothing to fail
// the backup over.
func (c *Client) DumpForBackup(a *db.App) ([]byte, error) {
	if a.PreBackupCmd == "" {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), dumpTimeout)
	defer cancel()

	out, err := c.CaptureCommand(ctx, a, a.PreBackupCmd)
	if errors.Is(err, ErrNoContainer) {
		return nil, nil
	}
	return out, err
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
// shares the traefik network with app containers). It returns "" when the app
// has no health path, or when a compose app gives us nothing to aim at.
func (c *Client) HealthURL(ctx context.Context, a *db.App) string {
	if a.HealthPath == "" {
		return ""
	}
	if !c.UsesCompose(a) {
		if a.Port <= 0 {
			return ""
		}
		return fmt.Sprintf("http://%s:%d%s", ContainerName(a.ID), a.Port, a.HealthPath)
	}

	// A compose app's containers are named by compose, not by Quasar, and the
	// port lives in the Traefik label its author wrote — which is more
	// trustworthy than a.Port, since nothing makes them agree.
	web, ok := c.composeWebContainer(ctx, a.ID)
	if !ok || len(web.Names) == 0 {
		return ""
	}
	port := a.Port
	for k, v := range web.Labels {
		if strings.HasSuffix(k, ".loadbalancer.server.port") {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				port = n
			}
			break
		}
	}
	if port <= 0 {
		return ""
	}
	// Container names come back from the API with a leading slash.
	return fmt.Sprintf("http://%s:%d%s", strings.TrimPrefix(web.Names[0], "/"), port, a.HealthPath)
}
