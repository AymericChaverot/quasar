// Package monitor runs the platform's background workers: container-state
// watching, HTTP health checks with auto-restart, metrics sampling and the
// scheduled-task runner.
package monitor

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"time"

	"quasar/internal/db"
	"quasar/internal/docker"
	"quasar/internal/notify"
	"quasar/internal/vps"
)

const (
	stateInterval   = 30 * time.Second
	healthInterval  = 30 * time.Second
	metricsInterval = 60 * time.Second
	taskInterval    = 60 * time.Second
	retention       = 7 * 24 * time.Hour
	failThreshold   = 3 // consecutive health failures before auto-restart
)

var healthClient = &http.Client{Timeout: 5 * time.Second}

// Start launches every background worker.
func Start(database *sql.DB, dock *docker.Client, hostRoot string) {
	go watchStates(database, dock)
	go checkHealth(database, dock)
	go sampleMetrics(database, dock, hostRoot)
	go runScheduledTasks(database, dock)
}

// watchStates notifies when an app enters or recovers from an error state.
func watchStates(database *sql.DB, dock *docker.Client) {
	last := map[string]string{} // app ID -> last observed state
	for {
		time.Sleep(stateInterval)
		apps, err := db.ListApps(database)
		if err != nil {
			continue
		}
		seen := map[string]bool{}
		for _, a := range apps {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			state := dock.Status(ctx, a).State
			cancel()
			seen[a.ID] = true

			prev, known := last[a.ID]
			last[a.ID] = state
			if !known || prev == state {
				continue
			}
			switch {
			case state == "error":
				notify.Send(database, fmt.Sprintf("Quasar: %s (%s.*) is in ERROR state", a.Name, a.Subdomain))
			case prev == "error" && state == "running":
				notify.Send(database, fmt.Sprintf("Quasar: %s (%s.*) recovered and is running again", a.Name, a.Subdomain))
			}
		}
		for id := range last {
			if !seen[id] {
				delete(last, id) // app was removed
			}
		}
	}
}

// checkHealth probes each app's health URL over the shared Docker network and
// restarts the container after too many consecutive failures.
func checkHealth(database *sql.DB, dock *docker.Client) {
	for {
		time.Sleep(healthInterval)
		apps, err := db.ListApps(database)
		if err != nil {
			continue
		}
		for _, a := range apps {
			if a.HealthPath == "" || a.DeployType == "compose" {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			running := dock.Status(ctx, a).State == "running"
			cancel()
			if !running {
				continue // don't count a deliberately stopped app as unhealthy
			}

			ok := probe(dock.HealthURL(a))
			db.RecordHealth(database, a.ID, ok)
			if ok {
				continue
			}
			if fails := db.ConsecutiveHealthFailures(database, a.ID); fails == failThreshold {
				ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
				err := dock.Restart(ctx, a)
				cancel()
				if err != nil {
					notify.Send(database, fmt.Sprintf("Quasar: %s failed %d health checks and could NOT be restarted: %v", a.Name, fails, err))
				} else {
					notify.Send(database, fmt.Sprintf("Quasar: %s failed %d health checks — container restarted automatically", a.Name, fails))
				}
			}
		}
	}
}

func probe(url string) bool {
	resp, err := healthClient.Get(url)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode < 500
}

// sampleMetrics stores server and per-app usage samples, pruning old data.
func sampleMetrics(database *sql.DB, dock *docker.Client, hostRoot string) {
	lastPrune := time.Now()
	for {
		time.Sleep(metricsInterval)
		if s, err := vps.Collect(hostRoot); err == nil {
			db.RecordServerMetric(database, s.CPUPercent, s.MemPercent, s.DiskPercent)
		}
		apps, err := db.ListApps(database)
		if err == nil {
			for _, a := range apps {
				if a.DeployType == "compose" {
					continue
				}
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				if st, err := dock.Stats(ctx, a); err == nil {
					db.RecordAppMetric(database, a.ID, st.CPUPercent, st.MemUsedMB)
				}
				cancel()
			}
		}
		if time.Since(lastPrune) > time.Hour {
			db.PruneTimeSeries(database, time.Now().Add(-retention))
			lastPrune = time.Now()
		}
	}
}

// runScheduledTasks executes due interval tasks inside their app containers.
func runScheduledTasks(database *sql.DB, dock *docker.Client) {
	for {
		time.Sleep(taskInterval)
		tasks, err := db.ListAllScheduledTasks(database)
		if err != nil {
			continue
		}
		now := time.Now()
		for _, t := range tasks {
			if !t.Due(now) {
				continue
			}
			a, err := db.GetApp(database, t.AppID)
			if err != nil {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			out, err := dock.RunCommand(ctx, a, t.Command)
			cancel()
			if err != nil {
				db.RecordTaskRun(database, t.ID, "failed", out+"\n"+err.Error())
				notify.Send(database, fmt.Sprintf("Quasar: scheduled task failed on %s: %s (%v)", a.Name, t.Command, err))
			} else {
				db.RecordTaskRun(database, t.ID, "success", out)
			}
		}
	}
}
