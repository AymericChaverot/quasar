package docker

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"

	"quasar/internal/db"
)

// Running a command in a named service, and reading a named service's logs.
//
// Everything else in this package that execs resolves one container through
// containerFor, which picks the compose project's web service or whatever the
// daemon listed first. That is the right answer for a shell button on an
// application's page and the wrong one here: a station's permission names the
// services it may reach, and a permission that resolved to "whichever" would
// name nothing.
//
// The other difference is argv rather than a shell string. RunCommand runs
// /bin/sh -c, which is fine for a command an operator typed; a station
// interpolates constantly — a mod filename, a player name, a value out of a
// config file — and a shell would turn the first one carrying a semicolon into
// an injection. Nothing here is ever parsed by a shell.

// ExecResult is what a command left behind.
type ExecResult struct {
	Code   int    `json:"code"`
	Stdout string `json:"stdout"`
	Stderr string `json:"stderr"`

	// Truncated says the output was longer than a script may be handed, and
	// says it out loud rather than quietly returning half a file.
	Truncated bool `json:"truncated"`
}

// MaxExecOutput caps one command's output. A station reading more than this
// out of a container is doing something a script should not be the shape of.
const MaxExecOutput = 1 << 20

// ErrNoService is returned when a stack has no such service running.
type ErrNoService struct {
	Service string
	Running []string
}

func (e *ErrNoService) Error() string {
	if len(e.Running) == 0 {
		return fmt.Sprintf("this application has no container for the service %q; nothing of it is running", e.Service)
	}
	return fmt.Sprintf("this application has no container for the service %q; it is running %s",
		e.Service, strings.Join(e.Running, ", "))
}

// containerForService resolves one container of an application by its compose
// service name.
//
// A single-image application has exactly one container and no service names to
// tell apart, so any name resolves to it: there is nothing else it could
// reach, and the name the permission carries is still the one the operator
// read on the install screen.
func (c *Client) containerForService(ctx context.Context, a *db.App, service string) (string, error) {
	if !c.UsesCompose(a) {
		return c.appContainer(ctx, a.ID)
	}
	var running []string
	for _, ct := range c.composeContainers(ctx, a.ID) {
		name := ct.Labels["com.docker.compose.service"]
		if name == service {
			return ct.ID, nil
		}
		if name != "" {
			running = append(running, name)
		}
	}
	return "", &ErrNoService{Service: service, Running: running}
}

// ServiceHost is the name a named service can be reached at from the
// dashboard, which shares the traefik network with application containers.
//
// It is resolved here and handed to the station rather than composed by the
// script, so a station never learns another container's name and has nothing
// to guess with: quasar.service returns an address for a service its document
// declared, or it returns nothing.
func (c *Client) ServiceHost(ctx context.Context, a *db.App, service string) (string, error) {
	if !c.UsesCompose(a) {
		if _, err := c.appContainer(ctx, a.ID); err != nil {
			return "", err
		}
		return ContainerName(a.ID), nil
	}
	for _, ct := range c.composeContainers(ctx, a.ID) {
		if ct.Labels["com.docker.compose.service"] != service {
			continue
		}
		if len(ct.Names) == 0 {
			return "", fmt.Errorf("the container for %q has no name", service)
		}
		// Container names come back from the API with a leading slash.
		return strings.TrimPrefix(ct.Names[0], "/"), nil
	}
	return "", &ErrNoService{Service: service}
}

// ExecInService runs argv inside one named service of an application.
//
// argv is passed to the daemon as it stands. There is no shell anywhere in
// this path, so a filename containing "; rm -rf /" is one argument with an
// awkward name in it.
func (c *Client) ExecInService(ctx context.Context, a *db.App, service string, argv []string, stdin string) (ExecResult, error) {
	if len(argv) == 0 {
		return ExecResult{}, fmt.Errorf("no command was given")
	}
	target, err := c.containerForService(ctx, a, service)
	if err != nil {
		return ExecResult{}, err
	}

	exec, err := c.api.ContainerExecCreate(ctx, target, container.ExecOptions{
		Cmd:          argv,
		AttachStdin:  stdin != "",
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return ExecResult{}, fmt.Errorf("exec create: %w", err)
	}
	resp, err := c.api.ContainerExecAttach(ctx, exec.ID, container.ExecStartOptions{})
	if err != nil {
		return ExecResult{}, fmt.Errorf("exec attach: %w", err)
	}
	defer resp.Close()

	if stdin != "" {
		io.WriteString(resp.Conn, stdin)
		resp.CloseWrite()
	}

	// One byte past the cap on each stream, which is the only way to tell
	// output that exactly fills it from output that overran.
	var stdout, stderr bytes.Buffer
	stdcopy.StdCopy(
		&limitedBuffer{buf: &stdout, left: MaxExecOutput + 1},
		&limitedBuffer{buf: &stderr, left: MaxExecOutput + 1},
		resp.Reader)

	out := ExecResult{Stdout: stdout.String(), Stderr: stderr.String()}
	if len(out.Stdout) > MaxExecOutput {
		out.Stdout, out.Truncated = out.Stdout[:MaxExecOutput], true
	}
	if len(out.Stderr) > MaxExecOutput {
		out.Stderr, out.Truncated = out.Stderr[:MaxExecOutput], true
	}

	inspect, err := c.api.ContainerExecInspect(ctx, exec.ID)
	if err != nil {
		return out, err
	}
	out.Code = inspect.ExitCode
	return out, nil
}

// TailLogs reads a named service's recent output and returns, rather than
// following it. A script is a function that runs and comes back, so there is
// nothing here for a stream to be delivered to.
func (c *Client) TailLogs(ctx context.Context, a *db.App, service string, tail int, since string) (string, error) {
	target, err := c.containerForService(ctx, a, service)
	if err != nil {
		return "", err
	}
	if tail <= 0 || tail > 5000 {
		tail = 200
	}
	rc, err := c.api.ContainerLogs(ctx, target, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Tail:       fmt.Sprint(tail),
		Since:      since,
		Timestamps: true,
	})
	if err != nil {
		return "", err
	}
	defer rc.Close()

	var buf bytes.Buffer
	w := &limitedBuffer{buf: &buf, left: MaxExecOutput}
	stdcopy.StdCopy(w, w, rc)
	return buf.String(), nil
}

// limitedBuffer keeps the first n bytes of a stream and drops the rest, so a
// container printing without pause cannot fill the dashboard's memory on a
// station's behalf.
type limitedBuffer struct {
	buf  *bytes.Buffer
	left int
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if l.left <= 0 {
		return len(p), nil
	}
	keep := p
	if len(keep) > l.left {
		keep = keep[:l.left]
	}
	l.left -= len(keep)
	l.buf.Write(keep)
	return len(p), nil
}

// execTimeout bounds one command a station runs, under the budget the call
// itself has, so a hung command comes back as a failed action rather than as a
// worker killed from outside.
const execTimeout = 55 * time.Second

// ExecContext is the context one station command runs under.
func ExecContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, execTimeout)
}
