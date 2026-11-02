package server

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"quasar/internal/db"
	"quasar/internal/secrets"
)

// The heaviest applications are drawn by name, bottom of the stack first, and
// the rest are summed into one band; the ranking is what each uses now.
func TestTopConsumersStackTheHeaviestAndSumTheRest(t *testing.T) {
	database := historyDB(t)
	keyring, err := secrets.LoadOrCreateKey(filepath.Join(t.TempDir(), "master.key"))
	if err != nil {
		t.Fatal(err)
	}
	// Eight applications, the n-th using n GB, plus one never sampled.
	for n := 1; n <= 8; n++ {
		id := fmt.Sprintf("app%d", n)
		if err := db.InsertApp(database, keyring, &db.App{ID: id, Name: id, Subdomain: id, DeployType: "image", ImageRef: "nginx"}); err != nil {
			t.Fatal(err)
		}
		for _, ago := range []string{"-30 minutes", "-1 minutes"} {
			if _, err := database.Exec(`INSERT INTO app_metrics (app_id, ts, cpu, mem_mb) VALUES (?, datetime('now', ?), ?, ?)`,
				id, ago, float64(9-n), float64(n*1024)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := db.InsertApp(database, keyring, &db.App{ID: "idle", Name: "idle", Subdomain: "idle", DeployType: "image", ImageRef: "nginx"}); err != nil {
		t.Fatal(err)
	}

	got := topConsumers(database, keyring, "mem", "1h", time.Hour, time.Now())
	if got.None || len(got.Chart.Plots) != topConsumersShown+1 {
		t.Fatalf("%d bands, want %d named and one for the rest", len(got.Chart.Plots), topConsumersShown)
	}
	if got.Chart.Plots[0].Label != "app8" || got.Chart.Plots[topConsumersShown].Label != "2 others" {
		t.Errorf("bands run %q … %q; want the heaviest first and the rest summed", got.Chart.Plots[0].Label, got.Chart.Plots[topConsumersShown].Label)
	}
	if got.Chart.Plots[topConsumersShown].Latest != "3 GB" {
		t.Errorf("the others band reads %q, want 1 + 2 GB", got.Chart.Plots[topConsumersShown].Latest)
	}
	if len(got.Ranking) != 8 || got.Ranking[0].Name != "app8" || got.Ranking[0].Value != "8.0 GB" || got.Ranking[0].Share != 100 {
		t.Errorf("ranking starts %+v; want app8 at 8 GB, the full bar", got.Ranking[0])
	}
	if last := got.Ranking[7]; last.Colour != othersColour || last.Share != 12.5 {
		t.Errorf("the lightest ranks as %+v; want the others' colour at an eighth of the bar", last)
	}

	cpu := topConsumers(database, keyring, "cpu", "1h", time.Hour, time.Now())
	if cpu.Ranking[0].Name != "app1" || cpu.Ranking[0].Value != "8.0%" {
		t.Errorf("by CPU the ranking starts %+v; want app1 at 8%%", cpu.Ranking[0])
	}
}

// With nothing sampled, the modal says so rather than drawing an empty chart.
func TestTopConsumersWithNothingSampled(t *testing.T) {
	database := historyDB(t)
	keyring, err := secrets.LoadOrCreateKey(filepath.Join(t.TempDir(), "master.key"))
	if err != nil {
		t.Fatal(err)
	}
	if got := topConsumers(database, keyring, "mem", "24h", 24*time.Hour, time.Now()); !got.None {
		t.Errorf("got %+v; want None", got)
	}
}
