package db

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"
)

// An application deployed from a station is an ordinary application carrying
// one extra field. Everything else about the row — the containers, the env,
// the backups — is what any application carries, which is what makes removing
// a station leave a working application behind.
func TestAnAppRemembersTheStationItCameFrom(t *testing.T) {
	database := openTestDB(t)
	keyring := testKeyring(t)

	from := &App{ID: "app1", Name: "Server", Subdomain: "server", DeployType: "compose", StationID: "minecraft-station"}
	plain := &App{ID: "app2", Name: "Blog", Subdomain: "blog", DeployType: "image"}
	for _, a := range []*App{from, plain} {
		if err := InsertApp(database, keyring, a); err != nil {
			t.Fatal(err)
		}
	}

	got, err := GetApp(database, keyring, "app1")
	if err != nil {
		t.Fatal(err)
	}
	if got.StationID != "minecraft-station" {
		t.Errorf("station_id = %q, want the station it was deployed from", got.StationID)
	}
	if got, err := GetApp(database, keyring, "app2"); err != nil || got.StationID != "" {
		t.Errorf("an application deployed the ordinary way carries station_id %q", got.StationID)
	}

	if n := CountAppsForStation(database, "minecraft-station"); n != 1 {
		t.Errorf("%d applications counted for the station, want 1", n)
	}
	if n := CountAppsForStation(database, "nothing"); n != 0 {
		t.Errorf("%d applications counted for a station nobody installed", n)
	}
}

// The navigation entry only appears once there is something behind it, and a
// disabled station is not something behind it.
func TestOnlyEnabledStationsCount(t *testing.T) {
	database := openTestDB(t)

	id, err := InsertStation(database, &Station{StationID: "demo", Name: "Demo", YAML: "schema: 1", PermsHash: "x", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if n := CountEnabledStations(database); n != 1 {
		t.Fatalf("%d enabled stations, want 1", n)
	}
	if err := SetStationEnabled(database, id, false); err != nil {
		t.Fatal(err)
	}
	if n := CountEnabledStations(database); n != 0 {
		t.Errorf("%d enabled stations after disabling the only one", n)
	}
}

// A store is scoped to an application and a station, so either one going is
// enough to orphan it, and both ends clear their side. What the test insists
// on is that each end clears only its own: two stations on one application and
// one station on two applications are both ordinary, and a delete that took
// the neighbour's rows with it would be discovered as a station that had
// forgotten everything the moment somebody removed a different one.
func TestRemovingEitherEndClearsTheStoreItOrphans(t *testing.T) {
	database := openTestDB(t)

	id, err := InsertStation(database, &Station{StationID: "minecraft", Name: "Minecraft", YAML: "schema: 1", PermsHash: "x", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct{ app, station string }{
		{"app1", "minecraft"},
		{"app2", "minecraft"},
		{"app1", "gitea"},
	} {
		if err := StationStoreSet(database, row.app, row.station, "seen", "yes"); err != nil {
			t.Fatal(err)
		}
	}

	if err := DeleteAppStore(database, "app2"); err != nil {
		t.Fatal(err)
	}
	if _, ok := StationStoreGet(database, "app2", "minecraft", "seen"); ok {
		t.Error("the store of a deleted application survived it")
	}
	if _, ok := StationStoreGet(database, "app1", "minecraft", "seen"); !ok {
		t.Error("deleting one application cleared another's store")
	}

	if err := DeleteStation(database, id); err != nil {
		t.Fatal(err)
	}
	if _, ok := StationStoreGet(database, "app1", "minecraft", "seen"); ok {
		t.Error("the store of a removed station survived it")
	}
	if _, ok := StationStoreGet(database, "app1", "gitea", "seen"); !ok {
		t.Error("removing one station cleared what another had remembered")
	}
}

// The store answers what an action decided last time; a series answers what
// the number was on Tuesday. Both are scoped to an application and a station,
// so both are cleared from the same two ends — and a series is bounded in a
// way the store is not, because a table nothing ever caps is a disk somebody
// else fills.
func TestASeriesIsBoundedAndScopedToItsPair(t *testing.T) {
	database := openTestDB(t)

	id, err := InsertStation(database, &Station{StationID: "minecraft", Name: "Minecraft", YAML: "schema: 1", PermsHash: "x", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}

	for i := range MaxSeriesNames {
		if err := RecordStationSeries(database, "app1", "minecraft", fmt.Sprintf("s%d", i), float64(i)); err != nil {
			t.Fatalf("recording series %d: %v", i, err)
		}
	}
	// The cap is on distinct names, not on samples: one more of what it
	// already keeps is ordinary, one more name is not.
	if err := RecordStationSeries(database, "app1", "minecraft", "s0", 99); err != nil {
		t.Errorf("a second sample of a series already kept was refused: %v", err)
	}
	err = RecordStationSeries(database, "app1", "minecraft", "one_too_many", 1)
	if err == nil {
		t.Error("a ninth series was accepted")
	} else if !strings.Contains(err.Error(), "s0") {
		t.Errorf("the refusal does not say what the series were spent on: %v", err)
	}
	// And the cap is per pair, so another application is not affected by it.
	if err := RecordStationSeries(database, "app2", "minecraft", "one_too_many", 1); err != nil {
		t.Errorf("another application's first series was refused: %v", err)
	}

	// What a name may be, and what a value may be. Both are checked here
	// rather than at the caller: the caller is a script.
	if err := RecordStationSeries(database, "app1", "minecraft", "Players Online", 1); err == nil {
		t.Error("a series name with spaces and capitals was accepted")
	}
	if err := RecordStationSeries(database, "app1", "minecraft", "s0", math.Inf(1)); err == nil {
		t.Error("an infinite value was accepted")
	}

	// Read back in order, and only what the window covers.
	points, err := StationSeries(database, "app1", "minecraft", "s0", time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 2 || points[1].Value != 99 {
		t.Errorf("s0 read back as %v, want two samples ending on 99", points)
	}
	if old, err := StationSeries(database, "app1", "minecraft", "s0", time.Now().Add(time.Hour)); err != nil || len(old) != 0 {
		t.Errorf("a window in the future held %v (%v)", old, err)
	}

	// Then both ends of the pair, each clearing only its own side.
	if err := DeleteAppSeries(database, "app2"); err != nil {
		t.Fatal(err)
	}
	if names, _ := StationSeriesNames(database, "app2", "minecraft"); len(names) != 0 {
		t.Errorf("a deleted application still has series: %v", names)
	}
	if names, _ := StationSeriesNames(database, "app1", "minecraft"); len(names) != MaxSeriesNames {
		t.Errorf("deleting one application took another's series: %v", names)
	}
	if err := DeleteStation(database, id); err != nil {
		t.Fatal(err)
	}
	if names, _ := StationSeriesNames(database, "app1", "minecraft"); len(names) != 0 {
		t.Errorf("a removed station still has series: %v", names)
	}
}

// A series does not end when it ages out of the window the rest of the
// platform keeps: it is folded into one row an hour and kept far longer. The
// fold has to survive being run twice — on a restart, on a clock that moved —
// because the way it fails otherwise is a graph that quietly doubles.
func TestSeriesAreFoldedIntoHoursRatherThanDropped(t *testing.T) {
	database := openTestDB(t)

	for _, v := range []float64{2, 8, 5} {
		if err := RecordStationSeries(database, "app1", "minecraft", "players", v); err != nil {
			t.Fatal(err)
		}
	}
	// Two of them in one hour and the third in the next, both far enough back
	// to be past the fold.
	old := time.Now().UTC().Add(-8 * 24 * time.Hour).Truncate(time.Hour)
	// Written in the format the column really holds, which is SQLite's own
	// CURRENT_TIMESTAMP: that is where every real row here gets its time, and
	// the fold reads the hour back out of it with strftime.
	stamp := func(t time.Time) string { return t.Format("2006-01-02 15:04:05") }
	if _, err := database.Exec(`UPDATE station_series SET ts = ?`, stamp(old.Add(10*time.Minute))); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE station_series SET ts = ? WHERE value = 5`, stamp(old.Add(90*time.Minute))); err != nil {
		t.Fatal(err)
	}

	fold := func() {
		t.Helper()
		if err := FoldStationSeries(database, time.Now().Add(-7*24*time.Hour), time.Now().Add(-365*24*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	fold()
	fold() // and again, which must change nothing

	var raw int
	database.QueryRow(`SELECT COUNT(*) FROM station_series`).Scan(&raw)
	if raw != 0 {
		t.Errorf("%d samples were left behind by the fold", raw)
	}

	hours, err := StationSeriesHourly(database, "app1", "minecraft", "players", old.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(hours) != 2 {
		t.Fatalf("%d folded hours, want 2: %v", len(hours), hours)
	}
	// The mean, and the least and the most it reached — which is the half an
	// average throws away and the half nobody can recover afterwards.
	if h := hours[0]; h.Avg != 5 || h.Min != 2 || h.Max != 8 || h.Samples != 2 {
		t.Errorf("the first hour folded to %+v, want avg 5 min 2 max 8 over 2 samples", h)
	}

	// And a read spanning both halves of the series finds it, without being
	// told where the seam is.
	points, err := StationSeries(database, "app1", "minecraft", "players", time.Now().Add(-30*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 2 || points[0].Value != 5 {
		t.Errorf("reading across the fold gave %v", points)
	}
	if names, _ := StationSeriesNames(database, "app1", "minecraft"); len(names) != 1 || names[0] != "players" {
		t.Errorf("a folded series is no longer one this station keeps: %v", names)
	}

	// Folded hours have a window of their own, and it is the fold that applies
	// it.
	if err := FoldStationSeries(database, time.Now(), time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if hours, _ := StationSeriesHourly(database, "app1", "minecraft", "players", old.Add(-time.Hour)); len(hours) != 0 {
		t.Errorf("folded hours older than their own window survived: %v", hours)
	}
}
