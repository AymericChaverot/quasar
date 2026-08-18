package server

import (
	"slices"
	"testing"
	"time"

	"quasar/internal/db"
	"quasar/internal/station"
)

// hookDoc is a station that asks for one of everything.
var hookDoc = station.Station{
	ID: "demo", Name: "Demo",
	Hooks: station.Hooks{
		AfterDeploy:  station.Hook{Action: "sync_config"},
		OnStart:      station.Hook{Action: "announce"},
		OnStop:       station.Hook{Action: "save_world"},
		OnHealthFail: station.Hook{Action: "collect_diagnostics"},
		Every:        []station.Schedule{{Minutes: 60, Action: "check_mod_updates"}},
	},
}

func freshWatch() *stationWatch { return &stationWatch{lastRun: map[string]time.Time{}} }

// The first look establishes a baseline. Firing on_start for everything that
// happened to be up when the dashboard restarted would make the hook mean
// "Quasar was restarted", which is not what anybody declared it for.
func TestTheFirstLookFiresNothing(t *testing.T) {
	watch := freshWatch()
	if got := dueHooks(hookDoc, watch, time.Now(), observed{status: "running"}); len(got) != 0 {
		t.Errorf("the first look fired %v", got)
	}
	if !watch.seen {
		t.Error("the first look did not record a baseline")
	}
}

// Starting and stopping are edges, and each one fires its own hook once.
func TestStartAndStopFireOnTheirEdges(t *testing.T) {
	watch := freshWatch()
	now := time.Now()
	dueHooks(hookDoc, watch, now, observed{status: "stopped"}) // the baseline

	got := dueHooks(hookDoc, watch, now, observed{status: "running"})
	if !slices.Contains(got, "announce") {
		t.Errorf("starting did not fire on_start: %v", got)
	}
	if got := dueHooks(hookDoc, watch, now, observed{status: "running"}); slices.Contains(got, "announce") {
		t.Error("on_start fired again while it stayed running")
	}

	got = dueHooks(hookDoc, watch, now, observed{status: "stopped"})
	if !slices.Contains(got, "save_world") {
		t.Errorf("stopping did not fire on_stop: %v", got)
	}
	if got := dueHooks(hookDoc, watch, now, observed{status: "stopped"}); slices.Contains(got, "save_world") {
		t.Error("on_stop fired twice for one stop")
	}
}

// A deploy that has stopped being one is a deploy that landed.
func TestAfterDeployFiresWhenTheDeployEnds(t *testing.T) {
	watch := freshWatch()
	now := time.Now()
	dueHooks(hookDoc, watch, now, observed{status: "deploying", deploying: true})

	got := dueHooks(hookDoc, watch, now, observed{status: "running"})
	if !slices.Contains(got, "sync_config") {
		t.Errorf("after_deploy did not fire: %v", got)
	}
	if got := dueHooks(hookDoc, watch, now, observed{status: "running"}); slices.Contains(got, "sync_config") {
		t.Error("after_deploy fired twice for one deploy")
	}
}

// The health hook fires on the edge, not on the state: a check that stays
// failed is one notification, not one every thirty seconds.
func TestHealthFailFiresOnce(t *testing.T) {
	watch := freshWatch()
	now := time.Now()
	dueHooks(hookDoc, watch, now, observed{status: "running"})

	got := dueHooks(hookDoc, watch, now, observed{status: "running", unhealthy: true})
	if !slices.Contains(got, "collect_diagnostics") {
		t.Errorf("on_health_fail did not fire: %v", got)
	}
	got = dueHooks(hookDoc, watch, now, observed{status: "running", unhealthy: true})
	if slices.Contains(got, "collect_diagnostics") {
		t.Error("on_health_fail fired again while it was still failing")
	}
}

// A stopped server has nothing to poll, and a fleet of stopped applications
// quietly burning CPU every minute is a bug people find on their hosting bill.
func TestScheduledActionsOnlyRunWhileTheApplicationIsRunning(t *testing.T) {
	watch := freshWatch()
	now := time.Now()

	dueHooks(hookDoc, watch, now, observed{status: "stopped"})
	if got := dueHooks(hookDoc, watch, now.Add(2*time.Hour), observed{status: "stopped"}); slices.Contains(got, "check_mod_updates") {
		t.Error("a stopped application ran its scheduled action")
	}
	if len(watch.lastRun) != 0 {
		t.Error("a stopped application was even scheduled")
	}
}

// And while it is running, it runs on the interval the document asked for
// rather than on every tick.
func TestScheduledActionsRunOnTheirInterval(t *testing.T) {
	watch := freshWatch()
	now := time.Now()
	running := observed{status: "running"}

	// The first look schedules rather than fires, so a restart is not a reason
	// for every station's six-hourly check to happen at once.
	if got := dueHooks(hookDoc, watch, now, running); slices.Contains(got, "check_mod_updates") {
		t.Error("a schedule fired on the look that established it")
	}
	if got := dueHooks(hookDoc, watch, now.Add(10*time.Minute), running); slices.Contains(got, "check_mod_updates") {
		t.Error("an hourly check ran after ten minutes")
	}
	if got := dueHooks(hookDoc, watch, now.Add(61*time.Minute), running); !slices.Contains(got, "check_mod_updates") {
		t.Errorf("the interval elapsed and nothing ran: %v", got)
	}
}

// A hook that fails is reported where every other privileged thing a station
// did already is, and nothing it was near fails with it.
func TestAFailingHookIsRecordedRatherThanRaised(t *testing.T) {
	s := brokerTestServer(t)
	a := &db.App{ID: "abcd1234", Name: "Server", StationID: "demo"}
	s.recordHookFailure(a, hookDoc, "sync_config", "no such file")

	entries, _ := db.ListAudit(s.db, "station.hook.fail", 10)
	if len(entries) != 1 {
		t.Fatalf("%d audit entries for a failed hook", len(entries))
	}
	if entries[0].Actor != "station demo" {
		t.Errorf("the failure is attributed to %q", entries[0].Actor)
	}
	if entries[0].Detail != "sync_config: no such file" {
		t.Errorf("the entry does not say what failed and why: %q", entries[0].Detail)
	}
}

// An application that is gone stops being watched, so a dashboard left running
// is not also a slow leak.
func TestWatchesAreDroppedWithTheirApplications(t *testing.T) {
	s := brokerTestServer(t)
	watches := map[string]*stationWatch{"gone-app": freshWatch()}

	s.runDueHooks(watches, time.Now())
	if _, held := watches["gone-app"]; held {
		t.Error("an application that no longer exists is still watched")
	}
}
