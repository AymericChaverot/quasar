package server

import (
	"context"
	"log"
	"time"

	"quasar/internal/db"
	"quasar/internal/station"
	"quasar/internal/station/ui"
)

// When a station's script runs without anybody having pressed anything.
//
// One loop watches the applications that came from a station and fires the
// hooks their documents declare: after a deploy lands, when the thing starts
// or stops, when its health check has been failing, and on whatever timer the
// document asked for.
//
// Hooks never block. A failing after_deploy is reported in the audit log and
// on the station's own tab; it does not fail the deployment. Third-party code
// on the critical path of a deploy is how a working site goes down for a
// reason nobody can find — so nothing here is on that path, and the loop that
// runs them is not the loop that deploys anything.
//
// Scheduled actions run only while the application is running. A stopped
// server has nothing to poll, and a fleet of stopped applications quietly
// burning CPU every minute is a bug people discover from their hosting bill.

// hookTick is how often the loop looks. Schedules are declared in minutes, so
// this is fine enough to be punctual and coarse enough to cost nothing.
const hookTick = 30 * time.Second

// healthFailThreshold is how many consecutive failed probes count as the
// health check having failed, matching what the monitor already alerts on.
const healthFailThreshold = 3

// stationWatch is what the loop remembers about one application between two
// looks. Nothing here is persisted: a hook that fired before a restart is a
// hook that fired, and one that would have is not worth replaying into a
// dashboard that has just come up.
type stationWatch struct {
	status    string
	deploying bool
	unhealthy bool

	// lastRun is when each scheduled action last went, so a document asking
	// for every six hours gets it every six hours rather than every tick.
	lastRun map[string]time.Time

	// seen marks a watch that has been looked at once, so the first look
	// establishes a baseline instead of firing on_start for everything that
	// happened to be running when the dashboard came up.
	seen bool
}

// StartStationHooks runs the loop for the life of the process.
func (s *Server) StartStationHooks() {
	go s.stationHookLoop()
}

func (s *Server) stationHookLoop() {
	watches := map[string]*stationWatch{}
	for {
		s.runDueHooks(watches, time.Now())
		time.Sleep(hookTick)
	}
}

// runDueHooks looks at every application deployed from a station and fires
// whatever its document asked for.
func (s *Server) runDueHooks(watches map[string]*stationWatch, now time.Time) {
	apps, err := db.ListApps(s.db, s.keyring)
	if err != nil {
		log.Printf("station hooks: listing applications: %v", err)
		return
	}

	live := map[string]bool{}
	for _, a := range apps {
		if a.StationID == "" {
			continue
		}
		doc, ok := s.stationFor(a)
		if !ok || doc.Hooks.Empty() {
			continue
		}
		live[a.ID] = true

		watch := watches[a.ID]
		if watch == nil {
			watch = &stationWatch{lastRun: map[string]time.Time{}}
			watches[a.ID] = watch
		}
		for _, action := range dueHooks(doc, watch, now, s.observe(a)) {
			s.runHook(a, doc, action)
		}
	}

	// An application that is gone, or whose station was removed, stops being
	// watched — otherwise the map is a slow leak on a long-running dashboard.
	for id := range watches {
		if !live[id] {
			delete(watches, id)
		}
	}
}

// observed is what one look at an application found.
type observed struct {
	status    string
	deploying bool
	unhealthy bool
}

// observe reads the application's current state, which is the only part of
// this that talks to anything.
func (s *Server) observe(a *db.App) observed {
	out := observed{status: "unknown"}
	if s.dock != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		out.status = s.dock.Status(ctx, a).State
		out.deploying = s.dock.Deploying(a.ID) != nil && out.status == "deploying"
		cancel()
	}
	out.unhealthy = a.HealthPath != "" && db.ConsecutiveHealthFailures(s.db, a.ID) >= healthFailThreshold
	return out
}

// dueHooks decides what to run, and updates the watch as it goes.
//
// It is the whole of the policy and it touches nothing: what it knows is the
// watch and one look, and what it returns is a list of action names. That is
// what makes "does a stopped application still poll" a question with a test
// rather than an argument.
func dueHooks(doc station.Station, watch *stationWatch, now time.Time, look observed) []string {
	status, deploying, unhealthy := look.status, look.deploying, look.unhealthy

	var run []string
	// The first look is a baseline. Firing on_start for every application that
	// happened to be up when the dashboard restarted would make a hook mean
	// "Quasar was restarted", which is not what anybody declared it for.
	if watch.seen {
		if status == "running" && watch.status != "running" {
			run = appendAction(run, doc.Hooks.OnStart.Action)
		}
		if status != "running" && watch.status == "running" {
			run = appendAction(run, doc.Hooks.OnStop.Action)
		}
		// A deploy that has stopped being one is a deploy that landed.
		if watch.deploying && !deploying {
			run = appendAction(run, doc.Hooks.AfterDeploy.Action)
		}
		// The edge, not the state: a health check that stays failed is one
		// notification, not one every thirty seconds.
		if unhealthy && !watch.unhealthy {
			run = appendAction(run, doc.Hooks.OnHealthFail.Action)
		}
	}
	watch.status, watch.deploying, watch.unhealthy, watch.seen = status, deploying, unhealthy, true

	// And the timers, which only run while there is something to poll.
	if status == "running" {
		for _, e := range doc.Hooks.Every {
			if e.Action == "" || e.Minutes < 1 {
				continue
			}
			last, ran := watch.lastRun[e.Action]
			if ran && now.Sub(last) < time.Duration(e.Minutes)*time.Minute {
				continue
			}
			watch.lastRun[e.Action] = now
			// The first look schedules rather than fires: a dashboard restart
			// should not be a reason for every station's six-hourly check to
			// happen at once.
			if ran {
				run = appendAction(run, e.Action)
			}
		}
	}
	return run
}

func appendAction(run []string, name string) []string {
	if name == "" {
		return run
	}
	return append(run, name)
}

// runHook runs one, and reports it going wrong rather than passing the failure
// on to whatever was happening at the time.
func (s *Server) runHook(a *db.App, doc station.Station, action string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	out, err := s.runStation(ctx, nil, a, doc, CallHook, action, map[string]any{})
	if err != nil {
		s.recordHookFailure(a, doc, action, stationProblem(action, err))
		return
	}
	if result := ui.ParseResult(out.Value); result.Error != "" {
		s.recordHookFailure(a, doc, action, result.Error)
	}
}

// recordHookFailure puts it where somebody will find it: the audit log, which
// is where every other privileged thing a station did already is.
func (s *Server) recordHookFailure(a *db.App, doc station.Station, action, problem string) {
	log.Printf("station %s on %s: hook %s failed: %s", doc.ID, a.Name, action, problem)
	db.RecordAudit(s.db, db.AuditEntry{
		Actor:  "station " + doc.ID,
		Action: "station.hook.fail",
		Target: doc.ID + " on " + a.Name,
		Detail: action + ": " + problem,
	})
}
