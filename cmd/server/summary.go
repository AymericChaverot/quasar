package main

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"quasar/internal/db"
	"quasar/internal/docker"
	"quasar/internal/event"
)

// logPlatformState writes, once the dashboard is up, how the platform it came
// up on is doing: which applications are running and which are not, and the
// state of the installed stations. It is the line to read after a restart of
// the server, when the question is whether everything came back with it.
//
// It asks Docker about every application in turn, so it runs after "ready"
// rather than in the sequence, where it would hold up the dashboard's start.
func logPlatformState(database *sql.DB, dock *docker.Client, apps []*db.App, dockerUp bool) {
	if len(apps) == 0 {
		event.Info("apps", "none deployed yet")
	} else if !dockerUp {
		event.Warning("apps", plural(len(apps), "application"), "state unknown until Docker answers")
	} else {
		states := map[string][]string{}
		for _, a := range apps {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			state := dock.Status(ctx, a).State
			cancel()
			states[state] = append(states[state], a.Name)
		}
		level, details := appsSummary(states)
		event.Print(level, "apps", details...)
	}

	stations, err := db.ListStations(database)
	if err != nil {
		event.Warning("stations", "listing them: "+err.Error())
		return
	}
	if len(stations) > 0 {
		level, details := stationsSummary(stations)
		event.Print(level, "stations", details...)
	}
}

// appOrder is the order the states are written in: the ones that are fine
// first, then the ones that need a look, which name the applications.
var appOrder = []struct {
	state, label string
	name         bool
	level        event.Level
}{
	{"running", "running", false, event.OK},
	{"stopped", "stopped", true, event.OK},
	{"not deployed", "not deployed", true, event.Warn},
	{"error", "in error", true, event.Fail},
}

// appsSummary turns the applications grouped by state into one line's
// details, and the level the worst of them calls for. A state with only a
// handful of applications names them, so the line says which to go and look
// at; running ones are only counted.
func appsSummary(states map[string][]string) (event.Level, []string) {
	level := event.OK
	var details []string
	add := func(names []string, label string, name bool) {
		d := fmt.Sprintf("%d %s", len(names), label)
		if name && len(names) <= 3 {
			sorted := append([]string(nil), names...)
			sort.Strings(sorted)
			d += " (" + strings.Join(sorted, ", ") + ")"
		}
		details = append(details, d)
	}
	for _, o := range appOrder {
		names := states[o.state]
		if len(names) == 0 {
			continue
		}
		add(names, o.label, o.name)
		level = max(level, o.level)
	}
	// Any state this list does not know yet is still counted, not dropped.
	var other []string
	for state := range states {
		known := false
		for _, o := range appOrder {
			known = known || o.state == state
		}
		if !known {
			other = append(other, state)
		}
	}
	sort.Strings(other)
	for _, state := range other {
		add(states[state], state, true)
	}
	return level, details
}

// stationsSummary counts the installed stations by whether they are enabled,
// and flags the updates waiting for somebody to approve them.
func stationsSummary(stations []*db.Station) (event.Level, []string) {
	var enabled, disabled, pending int
	for _, s := range stations {
		if s.Enabled {
			enabled++
		} else {
			disabled++
		}
		if s.PendingYAML != "" {
			pending++
		}
	}
	details := []string{fmt.Sprintf("%d enabled", enabled)}
	if disabled > 0 {
		details = append(details, fmt.Sprintf("%d disabled", disabled))
	}
	if pending == 0 {
		return event.OK, details
	}
	return event.Warn, append(details, plural(pending, "update")+" waiting for approval")
}
