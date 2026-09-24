package main

import (
	"strings"
	"testing"

	"quasar/internal/db"
	"quasar/internal/event"
)

func TestAppsSummary(t *testing.T) {
	tests := []struct {
		states map[string][]string
		level  event.Level
		want   string
	}{
		{
			map[string][]string{"running": {"a", "b", "c"}},
			event.OK, "3 running",
		},
		{
			map[string][]string{"running": {"a"}, "stopped": {"z", "m"}},
			event.OK, "1 running · 2 stopped (m, z)",
		},
		{
			map[string][]string{"running": {"a"}, "error": {"web"}, "not deployed": {"new"}},
			event.Fail, "1 running · 1 not deployed (new) · 1 in error (web)",
		},
		{
			map[string][]string{"stopped": {"a", "b", "c", "d"}},
			event.OK, "4 stopped",
		},
		{
			map[string][]string{"running": {"a"}, "paused": {"p"}},
			event.OK, "1 running · 1 paused (p)",
		},
	}
	for _, tt := range tests {
		level, details := appsSummary(tt.states)
		if got := strings.Join(details, " · "); got != tt.want || level != tt.level {
			t.Errorf("appsSummary(%v) = %v %q, want %v %q", tt.states, level, got, tt.level, tt.want)
		}
	}
}

func TestStationsSummary(t *testing.T) {
	level, details := stationsSummary([]*db.Station{
		{Enabled: true}, {Enabled: true, PendingYAML: "x"}, {Enabled: false},
	})
	if got := strings.Join(details, " · "); level != event.Warn || got != "2 enabled · 1 disabled · 1 update waiting for approval" {
		t.Errorf("got %v %q", level, got)
	}
	level, details = stationsSummary([]*db.Station{{Enabled: true}})
	if got := strings.Join(details, " · "); level != event.OK || got != "1 enabled" {
		t.Errorf("got %v %q", level, got)
	}
}
