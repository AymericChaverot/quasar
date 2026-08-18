package db

import "testing"

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
