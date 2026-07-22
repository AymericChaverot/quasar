package docker

import (
	"bufio"
	"context"
	"io"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"

	"quasar/internal/db"
)

// StreamLogs follows the app's logs and invokes send for each line until ctx
// is cancelled. Docker multiplexes stdout/stderr when the container has no
// TTY, so the stream goes through stdcopy before being split into lines.
func (c *Client) StreamLogs(ctx context.Context, a *db.App, send func(line string)) error {
	name, err := c.containerFor(ctx, a)
	if err != nil {
		send("(" + err.Error() + ")")
		return nil
	}

	rc, err := c.api.ContainerLogs(ctx, name, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     true,
		Tail:       "200",
		Timestamps: false,
	})
	if err != nil {
		return err
	}
	defer rc.Close()

	pr, pw := io.Pipe()
	go func() {
		_, err := stdcopy.StdCopy(pw, pw, rc)
		pw.CloseWithError(err)
	}()

	scanner := bufio.NewScanner(pr)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return nil
		default:
			send(scanner.Text())
		}
	}
	return nil
}
