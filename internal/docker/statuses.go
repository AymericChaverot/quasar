package docker

import (
	"context"
	"regexp"
	"sort"
	"strconv"
	"sync"

	"github.com/docker/docker/api/types/container"

	"quasar/internal/db"
)

// Statuses is Status for every application at once, from a single listing of
// the daemon's containers.
//
// The dashboard asked for each application's state on its own: a listing and
// an inspection per row, one after the other, every five seconds and for each
// tab it was open in. The listing already says whether each container runs and
// how a stopped one exited, so the only thing still asked of each is when a
// running one started — the uptime — and those are asked together.
//
// stack says which applications run as a compose project; the caller has
// already worked it out for each, and it means reading the checkout.
func (c *Client) Statuses(ctx context.Context, apps []*db.App, stack map[string]bool) map[string]AppStatus {
	out := make(map[string]AppStatus, len(apps))
	list, err := c.api.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		for _, a := range apps {
			out[a.ID] = AppStatus{State: "not deployed"}
		}
		return out
	}
	single := map[string][]container.Summary{}
	projects := map[string][]container.Summary{}
	for _, ct := range list {
		if id := ct.Labels[appLabel]; id != "" {
			single[id] = append(single[id], ct)
		}
		if p := ct.Labels["com.docker.compose.project"]; p != "" {
			projects[p] = append(projects[p], ct)
		}
	}

	var (
		mu sync.Mutex
		wg sync.WaitGroup
		// A handful at a time: the daemon answers each quickly, and a server
		// with fifty applications should not be sent fifty at once.
		slots = make(chan struct{}, 8)
	)
	for _, a := range apps {
		if d := c.Deploying(a.ID); d != nil && (d.Running || d.Err != "") {
			out[a.ID] = deployStatus(d)
			continue
		}
		if stack[a.ID] {
			out[a.ID] = composeStatusOf(projects[composeProject(a.ID)])
			continue
		}
		cts := single[a.ID]
		if len(cts) == 0 {
			out[a.ID] = AppStatus{State: "not deployed"}
			continue
		}
		sort.Slice(cts, func(i, j int) bool { return cts[i].Created > cts[j].Created })
		newest := cts[0]
		if newest.State != "running" {
			out[a.ID] = statusFromState(newest.State, "", exitCodeOf(newest.Status))
			continue
		}
		wg.Add(1)
		go func(appID, containerID string) {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			st := AppStatus{State: "running"}
			if info, err := c.api.ContainerInspect(ctx, containerID); err == nil {
				st = statusFromState(info.State.Status, info.State.StartedAt, info.State.ExitCode)
			}
			mu.Lock()
			out[appID] = st
			mu.Unlock()
		}(a.ID, newest.ID)
	}
	wg.Wait()
	return out
}

// deployStatus is what a deploy in flight, or one that has just failed, says
// in place of the containers.
func deployStatus(d *DeployState) AppStatus {
	if d.Running {
		return AppStatus{State: "deploying"}
	}
	return AppStatus{State: "error"}
}

var exitedStatus = regexp.MustCompile(`^Exited \((-?\d+)\)`)

// exitCodeOf reads the exit code out of a listing's status line, "Exited (137)
// 2 hours ago", which is the one thing an inspection would have been for.
func exitCodeOf(status string) int {
	m := exitedStatus.FindStringSubmatch(status)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}
