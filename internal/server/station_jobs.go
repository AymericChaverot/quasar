package server

import (
	"context"
	"path"
	"sync"
	"time"

	"quasar/internal/db"
	"quasar/internal/station"
	"quasar/internal/station/ui"
)

// Long actions.
//
// Upgrading a server or downloading forty mods does not fit inside an HTTP
// request. An action declared `long: true` is started here and the request
// comes straight back with a pane to watch it in — so a browser that gives up
// waiting, or a laptop that is closed, has not cancelled anything.
//
// What the pane shows is what the action wrote: quasar.progress and quasar.log
// arrive here as they happen rather than being gathered up and returned at the
// end, because a progress pane whose contents only appear once there is
// nothing left to wait for is a spinner with extra steps. The output stays
// afterwards, because the output is where somebody reads why the upgrade
// failed.

// maxJobLines is what one job's pane will hold. A script that writes without
// pause should not be able to fill the dashboard's memory, and nobody scrolls
// back a thousand lines of somebody else's progress.
const maxJobLines = 500

// jobRetention is how long a finished job stays readable. Long enough to come
// back to after making a cup of tea; short enough that a dashboard left
// running for a year is not also a log server.
const jobRetention = 30 * time.Minute

// stationJob is one long action, running or finished.
type stationJob struct {
	mu      sync.Mutex
	lines   []string
	running bool
	started time.Time
	result  ui.Result
	problem string
}

// snapshot is a job as the pane draws it. A copy, taken under the lock,
// because the goroutine writing it does not stop while a page renders.
type stationJobView struct {
	AppID   string
	Action  string
	Lines   []string
	Running bool
	Toast   string
	Problem string
	Elapsed string

	// Download is the file the finished job offered, as a link. A long action
	// is the one that produces something worth downloading — an archive of a
	// world takes minutes — and nobody is holding a request open for it, so the
	// file is offered in the pane rather than started for them. It is also the
	// only place it could be offered that survives a reload.
	Download     string
	DownloadName string
}

// Log receives one line while the call is still running. It is what makes this
// a progress pane rather than a report.
func (j *stationJob) Log(line string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if len(j.lines) < maxJobLines {
		j.lines = append(j.lines, line)
	}
}

func (j *stationJob) finish(result ui.Result, problem string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.running, j.result, j.problem = false, result, problem
}

func (j *stationJob) view(appID, action string) stationJobView {
	j.mu.Lock()
	defer j.mu.Unlock()
	v := stationJobView{
		AppID: appID, Action: action,
		Lines:   append([]string(nil), j.lines...),
		Running: j.running,
		Toast:   j.result.Toast,
		Problem: j.problem,
		Elapsed: time.Since(j.started).Round(time.Second).String(),
	}
	if v.Problem == "" {
		v.Problem = j.result.Error
	}
	// Held to the files permission when the job finished, so this is a path
	// the station was allowed to hand over rather than one it asked to.
	if j.result.Download != "" {
		v.Download = stationDownloadURL(appID, j.result.Download)
		v.DownloadName = path.Base(j.result.Download)
	}
	return v
}

// stationJobs is every long action this dashboard has run since it started.
//
// In memory rather than in the database, and deliberately: a job is a thing
// somebody is watching, and one that outlived a restart of the dashboard would
// be a progress pane for a process that is no longer running.
type stationJobRegistry struct {
	mu   sync.Mutex
	jobs map[string]*stationJob
}

func (r *stationJobRegistry) key(appID, action string) string { return appID + "\x00" + action }

// start registers a job unless one of the same name is already running: two
// upgrades of the same server at once is not a thing anybody meant to ask for.
func (r *stationJobRegistry) start(appID, action string) (*stationJob, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.jobs == nil {
		r.jobs = map[string]*stationJob{}
	}
	r.sweep()

	key := r.key(appID, action)
	if existing, ok := r.jobs[key]; ok {
		existing.mu.Lock()
		running := existing.running
		existing.mu.Unlock()
		if running {
			return existing, false
		}
	}
	job := &stationJob{running: true, started: time.Now()}
	r.jobs[key] = job
	return job, true
}

func (r *stationJobRegistry) get(appID, action string) *stationJob {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.jobs[r.key(appID, action)]
}

// sweep drops what nobody is coming back for. Called under the lock, from
// start, so it happens when there is something to do rather than on a timer.
func (r *stationJobRegistry) sweep() {
	for key, job := range r.jobs {
		job.mu.Lock()
		stale := !job.running && time.Since(job.started) > jobRetention
		job.mu.Unlock()
		if stale {
			delete(r.jobs, key)
		}
	}
}

// startLongAction runs an action detached from the request that asked for it.
//
// The context is the server's rather than the request's, which is the whole
// point: a browser that navigated away has not cancelled a mod download, and a
// deploy that stopped because somebody closed a laptop would be a worse thing
// than a slow one.
func (s *Server) startLongAction(a *db.App, doc station.Station, action string, input map[string]any) (*stationJob, bool) {
	job, fresh := s.jobs.start(a.ID, action)
	if !fresh {
		return job, false
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), longActionBudget)
		defer cancel()

		out, err := s.runStationJob(ctx, a, doc, action, input, job)
		if err != nil {
			job.finish(ui.Result{}, stationProblem(action, err))
			return
		}
		job.finish(s.allowDownload(a, doc, ui.ParseResult(out.Value)), "")
	}()
	return job, true
}

// longActionBudget bounds a long action from outside, above the worker's own
// wall clock, so a job that somehow outlives its worker still ends.
const longActionBudget = 30 * time.Minute
