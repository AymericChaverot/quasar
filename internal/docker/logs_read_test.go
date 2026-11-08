package docker

import (
	"context"
	"encoding/binary"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/moby/moby/client"

	"quasar/internal/db"
)

// logsDaemon is the fake daemon with a log endpoint: it answers the request
// for one container's logs with Docker's multiplexed frames, and remembers
// what it was asked.
type logsDaemon struct {
	*fakeDaemon
	tail, follow string
	lines        []string
}

func (d *logsDaemon) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.HasSuffix(r.URL.Path, "/logs") {
		d.fakeDaemon.ServeHTTP(w, r)
		return
	}
	d.tail, d.follow = r.URL.Query().Get("tail"), r.URL.Query().Get("follow")
	for _, l := range d.lines {
		payload := []byte(l + "\n")
		header := make([]byte, 8)
		header[0] = 1 // stdout
		binary.BigEndian.PutUint32(header[4:], uint32(len(payload)))
		_, _ = w.Write(append(header, payload...))
	}
}

func TestReadLogs(t *testing.T) {
	d := &logsDaemon{
		fakeDaemon: &fakeDaemon{containers: []map[string]any{
			ctr("c1", 100, "running", "Up 1 hour", map[string]string{appLabel: "a1"}),
		}},
		lines: []string{
			"2026-11-09T10:00:00.5Z GET / 200",
			"no stamp here",
		},
	}
	c := fakeClientFor(t, d)

	var got []LogLine
	ready := false
	err := c.ReadLogs(context.Background(), &db.App{ID: "a1", DeployType: "image"},
		LogOptions{Tail: 50, Ready: func() { ready = true }},
		func(l LogLine) {
			if !ready {
				t.Error("a line arrived before Ready")
			}
			got = append(got, l)
		})
	if err != nil {
		t.Fatal(err)
	}
	if d.tail != "50" || d.follow == "1" || d.follow == "true" {
		t.Errorf("asked Docker for tail=%q follow=%q, want tail=50 and no follow", d.tail, d.follow)
	}
	if len(got) != 2 || got[0].Text != "GET / 200" || got[0].TS.IsZero() ||
		got[1].Text != "no stamp here" || !got[1].TS.IsZero() {
		t.Errorf("lines = %+v", got)
	}
}

// With no container there is nothing to read, and the caller is told so
// rather than handed an empty log.
func TestReadLogsWithoutAContainer(t *testing.T) {
	c := fakeClient(t, &fakeDaemon{})
	err := c.ReadLogs(context.Background(), &db.App{ID: "a1", DeployType: "image"},
		LogOptions{Tail: 10}, func(LogLine) { t.Error("a line from nowhere") })
	if !errors.Is(err, ErrNoContainer) {
		t.Errorf("err = %v, want ErrNoContainer", err)
	}
}

// fakeClientFor is fakeClient for any handler standing in for the daemon.
func fakeClientFor(t *testing.T, h http.Handler) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	api, err := client.New(
		client.WithHost("tcp://"+srv.Listener.Addr().String()),
		client.WithAPIVersion("1.45"),
	)
	if err != nil {
		t.Fatal(err)
	}
	return &Client{api: api, appsDir: t.TempDir()}
}
