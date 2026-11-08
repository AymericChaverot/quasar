package docker

import (
	"bufio"
	"context"
	"github.com/moby/moby/client"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"

	"quasar/internal/db"
)

// LogLine is one line of container output with the moment the container wrote
// it — which is not the moment Quasar read it. The two differ by however long
// a batch sat in a buffer, and by the whole backlog when a stream is opened on
// a container that has been running for days.
//
// TS is zero when the daemon returned no usable timestamp, which callers show
// as an unstamped line rather than as the zero time.
type LogLine struct {
	TS   time.Time
	Text string
}

// StreamLogs follows the app's logs and invokes send for each line until ctx
// is cancelled. Docker multiplexes stdout/stderr when the container has no
// TTY, so the stream goes through stdcopy before being split into lines.
func (c *Client) StreamLogs(ctx context.Context, a *db.App, send func(LogLine)) error {
	name, err := c.containerFor(ctx, a)
	if err != nil {
		send(LogLine{TS: time.Now(), Text: "(" + err.Error() + ")"})
		return nil //nolint:nilerr // the reason went to the log pane; the stream itself did not fail
	}
	return c.StreamLogsByName(ctx, name, send)
}

// LogOptions are how much of a log to read, and whether to keep reading.
type LogOptions struct {
	Tail   int  // the most recent lines to start with
	Follow bool // keep the stream open for new lines until ctx is cancelled
	// Ready, when set, is called once Docker has accepted the request and
	// before the first line: the moment a caller knows there is a log to read.
	Ready func()
}

// ReadLogs reads the app's logs — its container, or the service a stack
// serves HTTP from, as on its page — and invokes send for each line. Unlike
// StreamLogs, a missing container is an error returned rather than a line in
// a pane: an API caller has no pane.
func (c *Client) ReadLogs(ctx context.Context, a *db.App, opts LogOptions, send func(LogLine)) error {
	name, err := c.containerFor(ctx, a)
	if err != nil {
		return err
	}
	return c.readLogs(ctx, name, opts, send)
}

// StreamLogsByName follows any container's logs by name — used for app
// containers, the services of a stack, and read-only system containers.
func (c *Client) StreamLogsByName(ctx context.Context, name string, send func(LogLine)) error {
	return c.readLogs(ctx, name, LogOptions{Tail: 200, Follow: true}, send)
}

func (c *Client) readLogs(ctx context.Context, name string, opts LogOptions, send func(LogLine)) error {
	rc, err := c.api.ContainerLogs(ctx, name, client.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     opts.Follow,
		Tail:       strconv.Itoa(opts.Tail),
		// The daemon prefixes each line with the time it received it, which is
		// the only timestamp available for a program that prints none of its
		// own — and the only correct one for a backlog replayed at once.
		Timestamps: true,
	})
	if err != nil {
		return err
	}
	defer rc.Close()
	if opts.Ready != nil {
		opts.Ready()
	}

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
			send(splitTimestamp(scanner.Text()))
		}
	}
	return nil
}

// splitTimestamp peels the RFC3339Nano prefix the daemon adds when asked for
// timestamps off a line.
//
// It is deliberately forgiving: a line whose prefix does not parse is returned
// whole and unstamped rather than losing its first word, because the prefix is
// only ever a convention about what the daemon happened to write.
func splitTimestamp(line string) LogLine {
	stamp, rest, ok := strings.Cut(line, " ")
	if !ok {
		return LogLine{Text: line}
	}
	ts, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
		return LogLine{Text: line}
	}
	return LogLine{TS: ts, Text: rest}
}
