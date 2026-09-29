package docker

import (
	"context"
	"fmt"

	"github.com/docker/docker/api/types/container"
)

// hostJobName is the container that hands a job to the host, and HostJobUnit
// the systemd unit the job then runs as on the host.
const (
	hostJobName = "quasar-host-job"
	HostJobUnit = "quasar-host-job"
)

// RunOnHost starts script as root on the host itself, as a transient systemd
// unit, and returns once systemd has taken it — not once it has finished. The
// script reports how it went through the files it writes.
//
// The dashboard has no way onto the host of its own: it sees the host's
// filesystem read-only and reaches Docker through the socket proxy. What it can
// do through that proxy is create containers, and a privileged one sharing the
// host's process namespace can enter the host through /proc/1/root. That is
// not a new power — anything allowed to create containers already has it —
// only this one use of it, made on purpose and audited.
//
// The job runs under the host's systemd rather than inside that container
// because a package upgrade may restart Docker, and restarting Docker kills
// every container: a package manager killed half-way through a transaction is
// how a server ends up needing a rescue console. As a systemd unit the job
// belongs to the host and outlives Docker, this dashboard, and the container
// that started it.
//
// systemd-run refuses a unit name already in use, so a second job cannot
// start while the first is still running, whatever the dashboard believes.
func (c *Client) RunOnHost(ctx context.Context, script string) error {
	// The dashboard's own image, which is on the host already: all it needs
	// is chroot, and busybox has one.
	self, err := c.api.ContainerInspect(ctx, "quasar-dashboard")
	if err != nil {
		return fmt.Errorf("find the dashboard's own image: %w", err)
	}

	c.removeContainer(ctx, hostJobName)
	created, err := c.api.ContainerCreate(ctx,
		&container.Config{
			Image: self.Config.Image,
			Entrypoint: []string{
				"chroot", "/proc/1/root",
				"systemd-run", "--unit=" + HostJobUnit, "--collect", "--quiet",
				"--", "/bin/sh", "-c",
			},
			Cmd:    []string{script},
			Labels: map[string]string{"quasar.updater": "host"},
		},
		&container.HostConfig{
			Privileged:  true,
			PidMode:     "host",
			NetworkMode: "none",
		},
		nil, nil, hostJobName)
	if err != nil {
		return fmt.Errorf("create the host job container: %w", err)
	}
	if err := c.api.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		return fmt.Errorf("start the host job container: %w", err)
	}

	statusCh, errCh := c.api.ContainerWait(ctx, created.ID, container.WaitConditionNotRunning)
	select {
	case err := <-errCh:
		return fmt.Errorf("watch the host job container: %w", err)
	case st := <-statusCh:
		if st.StatusCode != 0 {
			// Left behind on failure, like the updaters: `docker logs
			// quasar-host-job` is where the reason is.
			return fmt.Errorf("the host did not take the job: %s", c.lastLines(ctx, created.ID, 4))
		}
	}
	c.removeContainer(ctx, created.ID)
	return nil
}
