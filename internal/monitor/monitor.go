// Package monitor runs the platform's background workers: container-state
// watching, HTTP health checks with auto-restart, metrics sampling and the
// scheduled-task runner.
package monitor

import (
	"context"
	"database/sql"
	"fmt"
	"log"
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

	// seriesRetention is how long a station's own series are kept once folded
	// into hours. Longer than everything else because they cost so much less
	// once they are: a year of hourly rows for eight series is a few hundred
	// thousand of them, against the fourteen hundred a day per series the
	// samples themselves run to.
	seriesRetention = 365 * 24 * time.Hour
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
			if a.HealthPath == "" {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			running := dock.Status(ctx, a).State == "running"
			url := dock.HealthURL(ctx, a)
			cancel()
			if !running {
				continue // don't count a deliberately stopped app as unhealthy
			}
			if url == "" {
				continue // nothing to aim at (compose app with no labelled web service)
			}

			ok := probe(url)
			if err := db.RecordHealth(database, a.ID, ok); err != nil {
				log.Printf("monitor: recording the health of %s: %v", a.ID, err)
			}
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

// probe reports whether the app answered its health path. 4xx counts as a
// failure: a mistyped health path 404s, and accepting that meant the app was
// reported healthy forever no matter what state it was really in.
func probe(url string) bool {
	resp, err := healthClient.Get(url)
	if err != nil {
		return false
	}
	_ = resp.Body.Close() // the status code below is the whole answer
	return resp.StatusCode < 400
}

// sampleMetrics stores server and per-app usage samples, pruning old data.
func sampleMetrics(database *sql.DB, dock *docker.Client, hostRoot string, keyring *secrets.Keyring) {
	lastPrune := time.Now()
	alerts := newAlerter()
	for {
		time.Sleep(metricsInterval)
		if s, err := vps.Collect(hostRoot); err == nil {
			if err := db.RecordServerMetric(database, s.CPUPercent, s.MemPercent, s.DiskPercent); err != nil {
				log.Printf("monitor: recording the server sample: %v", err)
			}
			alerts.check(database, s)
		}
		apps, err := db.ListApps(database, keyring)
		if err == nil {
			for _, a := range apps {
				// Compose apps included: Stats sums their project, so a stack
				// is graphed like any other app instead of showing nothing.
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				if st, err := dock.Stats(ctx, a); err == nil {
					if err := db.RecordAppMetric(database, a.ID, st.CPUPercent, st.MemUsedMB); err != nil {
						log.Printf("monitor: recording the sample for %s: %v", a.ID, err)
					}
				}
				cancel()
			}
		}
		if time.Since(lastPrune) > time.Hour {
			if err := db.PruneTimeSeries(database, time.Now().Add(-retention)); err != nil {
				log.Printf("monitor: trimming the time series: %v", err)
			}
			// A station's series are folded rather than dropped: the same
			// seven days at full resolution, then one row an hour for a year.
			// A graph of a database growing, or of how many people were on a
			// server last month, is worth more than the samples it was drawn
			// from and costs a fraction of them.
			if err := db.FoldStationSeries(database,
				time.Now().Add(-retention), time.Now().Add(-seriesRetention)); err != nil {
				log.Printf("monitor: folding the stations' series: %v", err)
			}
			if err := db.PruneLogs(database); err != nil {
				log.Printf("monitor: trimming stored output: %v", err)
			}
			// Bounded by count rather than age: an audit trail is worth
			// keeping far longer than metrics, but not without limit.
			if err := db.PruneAudit(database); err != nil {
				log.Printf("monitor: trimming the audit log: %v", err)
			}
			if freed := db.Reclaim(database); freed > 0 {
				log.Printf("reclaimed %d MB of database space", freed>>20)
			}
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
	lines := make(chan db.LogEntry, 256)
	done := make(chan struct{})
	go func() {
		defer close(done)
		var buf []db.LogEntry
		ticker := time.NewTicker(logFlushInterval)
		defer ticker.Stop()
		flush := func() {
			if len(buf) == 0 {
				return
			}
			if err := db.AppendLogs(database, a.ID, buf); err != nil {
				log.Printf("monitor: storing %s output: %v", a.ID, err)
			}
			buf = buf[:0]
		}
		for {
			select {
			case entry, ok := <-lines:
				if !ok {
					flush()
					return
				}
				buf = append(buf, entry)
				if len(buf) >= logFlushBatch {
					flush()
				}
			case <-ticker.C:
				flush()
			}
		}
	}()
	// The stream ends when the context is cancelled or the container goes
	// away, and both are ordinary. An error worth a line says which.
	if err := dock.StreamLogs(ctx, a, func(l docker.LogLine) {
		lines <- db.LogEntry{TS: l.TS, Line: l.Text}
	}); err != nil && ctx.Err() == nil {
		log.Printf("monitor: following %s output: %v", a.ID, err)
	}
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
			status, detail := "success", out
			if err != nil {
				status, detail = "failed", out+"\n"+err.Error()
			}
			// The task ran; the row saying so is a separate promise. Losing it
			// is worth a line, and not worth skipping the alert below.
			if recErr := db.RecordTaskRun(database, t.ID, status, detail); recErr != nil {
				log.Printf("monitor: recording the run of task %d: %v", t.ID, recErr)
			}
			if err != nil {
				notify.Send(database, fmt.Sprintf("Quasar: scheduled task failed on %s: %s (%v)", a.Name, t.Command, err))
			}
		}
	}
}
