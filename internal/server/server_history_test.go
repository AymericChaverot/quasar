package server

import (
	"database/sql"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"quasar/internal/db"
)

func historyDB(t *testing.T) *sql.DB {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "quasar.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	for _, ago := range []string{"-50 minutes", "-20 minutes"} {
		if _, err := database.Exec(`INSERT INTO metrics (ts, cpu, mem, disk) VALUES (datetime('now', ?), 30, 50, 70)`, ago); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.RecordChartEvent(database, db.EventRestart, "a", "Auto-restart · blog"); err != nil {
		t.Fatal(err)
	}
	return database
}

func toggles(keys ...string) []Measurement {
	var out []Measurement
	for _, m := range ServerToggles {
		for _, k := range keys {
			if m.Key == k {
				out = append(out, m)
			}
		}
	}
	return out
}

// Every toggle turns its own part of the chart on and off, and nothing else.
func TestEachToggleDrawsItsOwnPartOfTheServerChart(t *testing.T) {
	database := historyDB(t)
	now := time.Now()

	all := serverHistory(database, ServerToggles, "1h", time.Hour, now)
	v := all.Chart
	if all.Nothing || len(v.Plots) != 3 || v.Plots[0].Band == "" || v.Plots[2].Band != "" {
		t.Fatalf("everything on: %d plots, cpu band %q, disk band %q", len(v.Plots), v.Plots[0].Band, v.Plots[2].Band)
	}
	if len(v.Rules) != 2 || v.Rules[0].Label != "CPU / Memory alert" || v.Rules[1].Label != "Disk alert" {
		t.Errorf("rules = %+v; want the CPU and memory alerts drawn once together, then the disk's", v.Rules)
	}
	if len(v.Marks) != 1 || v.Marks[0].Label != "Auto-restart · blog" {
		t.Errorf("marks = %+v", v.Marks)
	}
	if v.Plots[1].Colour != "var(--chart-memory)" || v.Legend {
		t.Errorf("memory drawn in %q, legend %v; want its own colour and the toggles as the legend", v.Plots[1].Colour, v.Legend)
	}

	bare := serverHistory(database, toggles("disk"), "1h", time.Hour, now).Chart
	if len(bare.Plots) != 1 || bare.Plots[0].Label != "Disk" || len(bare.Rules) != 0 || len(bare.Marks) != 0 {
		t.Errorf("disk alone: %+v", bare)
	}

	cpuAlert := serverHistory(database, toggles("cpu", "alerts"), "1h", time.Hour, now).Chart
	if len(cpuAlert.Rules) != 1 || cpuAlert.Rules[0].Label != "CPU alert" || cpuAlert.Plots[0].Band != "" {
		t.Errorf("cpu with its alert: rules %+v, band %q", cpuAlert.Rules, cpuAlert.Plots[0].Band)
	}

	if none := serverHistory(database, toggles("range", "events"), "1h", time.Hour, now); !none.Nothing {
		t.Error("with every measurement off, the chart should say nothing is selected")
	}
}

// An alert turned off draws no rule.
func TestAnAlertTurnedOffDrawsNoRule(t *testing.T) {
	database := historyDB(t)
	if err := db.SetSetting(database, db.SettingAlertDisk, "0"); err != nil {
		t.Fatal(err)
	}
	v := serverHistory(database, toggles("disk", "alerts"), "1h", time.Hour, time.Now()).Chart
	if len(v.Rules) != 0 {
		t.Errorf("rules = %+v; the disk alert is off", v.Rules)
	}
}

// The dashboard's toggles open on what was saved, and the layers are on for
// somebody who never chose.
func TestTheTogglesOpenOnTheSavedChoice(t *testing.T) {
	fresh := picker(httptest.NewRequest("GET", "/", nil), ServerPicker, ServerToggles)
	for _, c := range fresh {
		if !c.Checked {
			t.Errorf("%s is off for somebody who never chose", c.Key)
		}
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Cookie", showCookie(ServerPicker)+"=cpu,events")
	for _, c := range picker(r, ServerPicker, ServerToggles) {
		if c.Checked != (c.Key == "cpu" || c.Key == "events") {
			t.Errorf("%s checked = %v with cpu and events saved", c.Key, c.Checked)
		}
	}
}
