package docker

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/docker/docker/client"

	"quasar/internal/db"
)

// fakeDaemon answers the two calls a state is read from: the container
// listing, filtered by label the way the Engine filters it, and an inspection.
type fakeDaemon struct {
	containers []map[string]any
	started    map[string]string // container ID -> StartedAt
	lists      atomic.Int32
	inspects   atomic.Int32
	diskUsages atomic.Int32
}

func (f *fakeDaemon) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path[strings.Index(r.URL.Path[1:], "/")+1:] // drop /v1.xx
	switch {
	case path == "/system/df":
		f.diskUsages.Add(1)
		_, _ = w.Write([]byte(`{"LayersSize": 1000, "Images": [], "Containers": [], "Volumes": [], "BuildCache": []}`))
	case path == "/networks":
		_, _ = w.Write([]byte(`[]`))
	case path == "/containers/json":
		f.lists.Add(1)
		var want string
		if raw := r.URL.Query().Get("filters"); raw != "" {
			var fl map[string]map[string]bool
			_ = json.Unmarshal([]byte(raw), &fl)
			for k := range fl["label"] {
				want = k
			}
		}
		out := []map[string]any{}
		for _, ct := range f.containers {
			if want != "" {
				k, v, _ := strings.Cut(want, "=")
				if ct["Labels"].(map[string]string)[k] != v {
					continue
				}
			}
			out = append(out, ct)
		}
		_ = json.NewEncoder(w).Encode(out)
	case strings.HasPrefix(path, "/containers/") && strings.HasSuffix(path, "/json"):
		f.inspects.Add(1)
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/containers/"), "/json")
		for _, ct := range f.containers {
			if ct["Id"] != id {
				continue
			}
			code := 0
			if s, _ := ct["Status"].(string); strings.HasPrefix(s, "Exited (") {
				code = exitCodeOf(s)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Id": id,
				"State": map[string]any{
					"Status": ct["State"], "StartedAt": f.started[id], "ExitCode": code,
				},
			})
			return
		}
		http.NotFound(w, r)
	default:
		http.NotFound(w, r)
	}
}

func fakeClient(t *testing.T, f *fakeDaemon) *Client {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	api, err := client.NewClientWithOpts(
		client.WithHost("tcp://"+srv.Listener.Addr().String()),
		client.WithVersion("1.45"),
	)
	if err != nil {
		t.Fatal(err)
	}
	return &Client{api: api, appsDir: t.TempDir()}
}

func ctr(id string, created int64, state, status string, labels map[string]string) map[string]any {
	return map[string]any{"Id": id, "Created": created, "State": state, "Status": status, "Labels": labels}
}

// Every application's state from one listing is the same state Status reads
// for each on its own — running with its uptime, stopped, crashed, a stack
// partly up, and one never deployed.
func TestStatusesAgreesWithStatus(t *testing.T) {
	started := time.Now().Add(-3 * time.Hour).UTC().Format(time.RFC3339Nano)
	f := &fakeDaemon{
		containers: []map[string]any{
			// web runs; an older container from the previous deploy is gone.
			ctr("web-old", 100, "exited", "Exited (0) 1 hour ago", map[string]string{appLabel: "web"}),
			ctr("web-new", 200, "running", "Up 3 hours", map[string]string{appLabel: "web"}),
			ctr("idle", 100, "exited", "Exited (0) 2 hours ago", map[string]string{appLabel: "idle"}),
			ctr("crash", 100, "exited", "Exited (137) 5 minutes ago", map[string]string{appLabel: "crash"}),
			ctr("st-db", 100, "running", "Up 1 hour", map[string]string{"com.docker.compose.project": composeProject("stack")}),
			ctr("st-web", 100, "exited", "Exited (1) 1 minute ago", map[string]string{"com.docker.compose.project": composeProject("stack")}),
			ctr("other", 100, "running", "Up 1 hour", map[string]string{}),
		},
		started: map[string]string{"web-new": started},
	}
	c := fakeClient(t, f)
	apps := []*db.App{
		{ID: "web", DeployType: "image"},
		{ID: "idle", DeployType: "image"},
		{ID: "crash", DeployType: "image"},
		{ID: "stack", DeployType: "compose"},
		{ID: "never", DeployType: "image"},
	}
	stack := map[string]bool{"stack": true}

	f.lists.Store(0)
	f.inspects.Store(0)
	got := c.Statuses(t.Context(), apps, stack)
	if n := f.lists.Load(); n != 1 {
		t.Errorf("Statuses listed containers %d times, want once", n)
	}
	if n := f.inspects.Load(); n != 1 {
		t.Errorf("Statuses inspected %d containers, want only the one running", n)
	}

	want := map[string]string{"web": "running", "idle": "stopped", "crash": "error", "stack": "error", "never": "not deployed"}
	for _, a := range apps {
		if got[a.ID].State != want[a.ID] {
			t.Errorf("%s: state %q, want %q", a.ID, got[a.ID].State, want[a.ID])
		}
		if one := c.Status(t.Context(), a); one != got[a.ID] {
			t.Errorf("%s: Statuses says %+v, Status says %+v", a.ID, got[a.ID], one)
		}
	}
	if got["web"].Uptime != "3h 0m" {
		t.Errorf("web uptime %q, want 3h 0m", got["web"].Uptime)
	}
}

func TestExitCodeOf(t *testing.T) {
	for status, want := range map[string]int{
		"Exited (137) 2 hours ago": 137,
		"Exited (0) 1 second ago":  0,
		"Up 3 hours":               0,
		"Created":                  0,
	} {
		if got := exitCodeOf(status); got != want {
			t.Errorf("exitCodeOf(%q) = %d, want %d", status, got, want)
		}
	}
}
