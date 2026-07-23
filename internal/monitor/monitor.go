// Package monitor runs the platform's background workers: container-state
// watching, HTTP health checks with auto-restart, metrics sampling and the
// scheduled-task runner.
package monitor

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"sync"
	"time"

	"quasar/internal/db"
	"quasar/internal/docker"
	"quasar/internal/notify"
	"quasar/internal/secrets"
	"quasar/internal/vps"
)

const (
	stateInterval   = 30 * time.Second
	healthInterval  = 30 * time.Second
	metricsInterval = 60 * time.Second
	taskInterval    = 60 * time.Second
	retention       = 7 * 24 * time.Hour
	failThreshold   = 3 // consecutive health failures before auto-restart

	logRescanInterval = stateInterval
	logFlushInterval  = 2 * time.Second
	logFlushBatch     = 200
)

var healthClient = &http.Client{Timeout: 5 * time.Second}

// Start launches every background worker.
func Start(database *sql.DB, dock *docker.Client, hostRoot string, keyring *secrets.Keyring) {
	go watchStates(database, dock, keyring)
	go checkHealth(database, dock, keyring)
	go sampleMetrics(database, dock, hostRoot, keyring)
	go runScheduledTasks(database, dock, keyring)
	go captureLogs(database, dock, keyring)
}

// watchStates notifies when an app enters or recovers from an error state.
func watchStates(database *sql.DB, dock *docker.Client, keyring *secrets.Keyring) {
	last := map[string]string{} // app ID -> last observed state
	for {
		time.Sleep(stateInterval)
		apps, err := db.ListApps(database, keyring)
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
func checkHealth(database *sql.DB, dock *docker.Client, keyring *secrets.Keyring) {
	for {
		time.Sleep(healthInterval)
		apps, err := db.ListApps(database, keyring)
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
func sampleMetrics(database *sql.DB, dock *docker.Client, hostRoot string, keyring *secrets.Keyring) {
	lastPrune := time.Now()
	for {
		time.Sleep(metricsInterval)
		if s, err := vps.Collect(hostRoot); err == nil {
			db.RecordServerMetric(database, s.CPUPercent, s.MemPercent, s.DiskPercent)
		}
		apps, err := db.ListApps(database, keyring)
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

// logCapture tracks which apps currently have a log-streaming goroutine
// running, so captureLogs can start one per running app and let it clean up
// its own entry when the container stops (making it eligible to restart on
// the next rescan).
type logCapture struct {
	mu     sync.Mutex
	active map[string]context.CancelFunc
}

func (lc *logCapture) running(id string) bool {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	_, ok := lc.active[id]
	return ok
}

func (lc *logCapture) start(database *sql.DB, dock *docker.Client, a *db.App) {
	lc.mu.Lock()
	if _, ok := lc.active[a.ID]; ok {
		lc.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	lc.active[a.ID] = cancel
	lc.mu.Unlock()

	go func() {
		streamAppLogs(ctx, database, dock, a)
		lc.mu.Lock()
		delete(lc.active, a.ID)
		lc.mu.Unlock()
	}()
}

// stopMissing cancels streams for apps that no longer exist (deleted since
// the last scan); apps that merely stopped are left for their own goroutine
// to exit and deregister once the container's log stream ends.
func (lc *logCapture) stopMissing(seen map[string]bool) {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	for id, cancel := range lc.active {
		if !seen[id] {
			cancel()
			delete(lc.active, id)
		}
	}
}

// captureLogs persists each running app's container output so history
// survives past a live SSE session and can be searched later, on the
// system-wide Logs page.
func captureLogs(database *sql.DB, dock *docker.Client, keyring *secrets.Keyring) {
	lc := &logCapture{active: map[string]context.CancelFunc{}}
	for {
		time.Sleep(logRescanInterval)
		apps, err := db.ListApps(database, keyring)
		if err != nil {
			continue
		}
		seen := map[string]bool{}
		for _, a := range apps {
			seen[a.ID] = true
			if lc.running(a.ID) {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			running := dock.Status(ctx, a).State == "running"
			cancel()
			if running {
				lc.start(database, dock, a)
			}
		}
		lc.stopMissing(seen)
	}
}

// streamAppLogs follows one app's container output and batches it into
// storage; it returns once the container stops (or the app is removed).
func streamAppLogs(ctx context.Context, database *sql.DB, dock *docker.Client, a *db.App) {
	lines := make(chan string, 256)
	done := make(chan struct{})
	go func() {
		defer close(done)
		var buf []string
		ticker := time.NewTicker(logFlushInterval)
		defer ticker.Stop()
		flush := func() {
			if len(buf) == 0 {
				return
			}
			db.AppendLogs(database, a.ID, buf)
			buf = buf[:0]
		}
		for {
			select {
			case line, ok := <-lines:
				if !ok {
					flush()
					return
				}
				buf = append(buf, line)
				if len(buf) >= logFlushBatch {
					flush()
				}
			case <-ticker.C:
				flush()
			}
		}
	}()
	dock.StreamLogs(ctx, a, func(line string) { lines <- line })
	close(lines)
	<-done
}

// runScheduledTasks executes due interval tasks inside their app containers.
func runScheduledTasks(database *sql.DB, dock *docker.Client, keyring *secrets.Keyring) {
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
			a, err := db.GetApp(database, keyring, t.AppID)
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
