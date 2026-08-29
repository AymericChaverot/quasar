package server

import (
	"bytes"
	"context"
	"encoding/json"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"quasar/internal/config"
	"quasar/internal/db"
	"quasar/internal/secrets"
	"quasar/internal/station"
)

// brokerTestServer is a dashboard with a real database, a real master key and a
// real apps directory, because what these tests are about is a capability
// crossing from a worker that holds none of those into a parent that does.
func brokerTestServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	keyring, err := secrets.LoadOrCreateKey(filepath.Join(dir, "master.key"))
	if err != nil {
		t.Fatal(err)
	}
	return &Server{
		db:      database,
		keyring: keyring,
		cfg:     config.Config{AppsDir: filepath.Join(dir, "apps"), Domain: "example.com"},
		pages:   map[string]*template.Template{},
		guards:  map[string]string{},
		mux:     http.NewServeMux(),
	}
}

// brokerFor sets up an application with a folder on disk and a station with
// the permissions the test is about.
func brokerFor(t *testing.T, perms station.Permissions, env string) (*stationCall, string) {
	t.Helper()
	s := brokerTestServer(t)
	app := &db.App{ID: "abcd1234", Name: "Server", Subdomain: "server", DeployType: "compose",
		EnvContent: env, StationID: "demo"}
	if err := db.InsertApp(s.db, s.keyring, app); err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(s.cfg.AppsDir, app.ID)
	if err := os.MkdirAll(filepath.Join(dir, "data", "mods"), 0o755); err != nil {
		t.Fatal(err)
	}
	doc := station.Station{ID: "demo", Name: "Demo", Permissions: perms}
	return &stationCall{srv: s, app: app, doc: doc}, dir
}

// ask performs one capability the way a worker would.
func ask(t *testing.T, c *stationCall, capability string, args map[string]any) (json.RawMessage, error) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return c.Do(context.Background(), capability, raw)
}

// The declared globs are the whole of what a station may touch. An
// application's folder holds its database, its uploads and its secrets, and
// none of that is what an operator meant to hand over by ticking "files".
func TestFilesOutsideTheDeclaredGlobsAreRefused(t *testing.T) {
	c, dir := brokerFor(t, station.Permissions{Files: station.Files{
		Paths: []string{"data/mods/**", "data/server.properties"},
	}}, "")
	os.WriteFile(filepath.Join(dir, "data", "server.properties"), []byte("motd=hello\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "data", "secrets.json"), []byte(`{"token":"hunter2"}`), 0o600)

	if _, err := ask(t, c, "files.read", map[string]any{"path": "data/server.properties"}); err != nil {
		t.Fatalf("a declared path was refused: %v", err)
	}
	for _, path := range []string{
		"data/secrets.json",                // a sibling nobody declared
		"data/mods/../secrets.json",        // the same one, reached sideways
		"../../etc/passwd",                 // straight out
		"/etc/passwd",                      // out by way of an absolute path
		"data/mods/../../../../etc/passwd", // out from inside a match
	} {
		if _, err := ask(t, c, "files.read", map[string]any{"path": path}); err == nil {
			t.Errorf("%s was readable", path)
		} else if !strings.Contains(err.Error(), "files") {
			t.Errorf("%s: the refusal does not name the permission: %v", path, err)
		}
	}
}

// A symlink is the case the string check alone would miss: an application's
// containers write these trees, and one of them is free to drop in a link
// pointing anywhere on the host.
func TestASymlinkOutOfTheFolderIsRefused(t *testing.T) {
	c, dir := brokerFor(t, station.Permissions{Files: station.Files{
		Paths: []string{"data/**"},
	}}, "")

	outside := filepath.Join(t.TempDir(), "elsewhere.txt")
	os.WriteFile(outside, []byte("not yours"), 0o644)
	link := filepath.Join(dir, "data", "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("this machine does not allow symlinks: %v", err)
	}

	if _, err := ask(t, c, "files.read", map[string]any{"path": "data/escape"}); err == nil {
		t.Error("a symlink out of the application's folder was followed")
	}
	// And writing through one replaces the link rather than the target, which
	// is the storage explorer's own guarantee showing through.
	ask(t, c, "files.write", map[string]any{"path": "data/escape", "content": []byte("mine")})
	if got, _ := os.ReadFile(outside); string(got) != "not yours" {
		t.Errorf("the write went through the link: the target now reads %q", got)
	}
}

func TestFilesRoundTripWithinTheGlobs(t *testing.T) {
	c, dir := brokerFor(t, station.Permissions{Files: station.Files{Paths: []string{"data/mods/**"}}}, "")

	// A real jar, in the only respect that matters here: it opens with the two
	// bytes every zip does and goes on to hold bytes that are not text at all.
	// Written through a JSON string, every one of those would reach the disk
	// as U+FFFD and the file would be a jar nothing could open.
	jar := []byte{'P', 'K', 0x03, 0x04, 0x00, 0xFF, 0xFE, 0x80, '\n', 0xC3}
	if _, err := ask(t, c, "files.write", map[string]any{
		"path": "data/mods/sodium.jar", "content": jar,
	}); err != nil {
		t.Fatalf("writing: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "data", "mods", "sodium.jar")); err != nil || !bytes.Equal(got, jar) {
		t.Fatalf("the file on disk is %v (%v), want %v", got, err, jar)
	}

	// And back out again, which is the same journey in reverse.
	read, err := ask(t, c, "files.readBytes", map[string]any{"path": "data/mods/sodium.jar"})
	if err != nil {
		t.Fatalf("reading the bytes back: %v", err)
	}
	var back []byte
	if err := json.Unmarshal(read, &back); err != nil {
		t.Fatalf("readBytes did not answer with bytes: %v (%s)", err, read)
	}
	if !bytes.Equal(back, jar) {
		t.Errorf("readBytes returned %v, want %v", back, jar)
	}

	listed, err := ask(t, c, "files.list", map[string]any{"path": "data/mods"})
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	var entries []struct {
		Name string `json:"name"`
		Dir  bool   `json:"dir"`
	}
	json.Unmarshal(listed, &entries)
	if len(entries) != 1 || entries[0].Name != "sodium.jar" || entries[0].Dir {
		t.Errorf("listing = %s", listed)
	}

	if _, err := ask(t, c, "files.delete", map[string]any{"path": "data/mods/sodium.jar"}); err != nil {
		t.Fatalf("deleting: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "data", "mods", "sodium.jar")); err == nil {
		t.Error("the file is still there")
	}

	// Every privileged thing a station did has an entry, so "what did this
	// station do to my server" has an answer.
	entriesLog, _ := db.ListAudit(c.srv.db, "station.files", 10)
	if len(entriesLog) != 2 {
		t.Errorf("%d audit entries for a write and a delete", len(entriesLog))
	}
}

// Per key and per direction. A station that generated a token may read it
// back; one that did not has no business seeing a database password.
func TestEnvIsRefusedForKeysTheDocumentDidNotName(t *testing.T) {
	c, _ := brokerFor(t, station.Permissions{Env: station.Env{
		Read:  []string{"MINECRAFT_VERSION"},
		Write: []string{"MINECRAFT_VERSION"},
	}}, "# the stack's own\nMINECRAFT_VERSION=1.20.1\nRCON_PASSWORD=hunter2\n")

	value, err := ask(t, c, "env.get", map[string]any{"key": "MINECRAFT_VERSION"})
	if err != nil || string(value) != `"1.20.1"` {
		t.Fatalf("a declared key read back as %s (%v)", value, err)
	}

	for _, capability := range []string{"env.get", "env.set"} {
		_, err := ask(t, c, capability, map[string]any{"key": "RCON_PASSWORD", "value": "x"})
		if err == nil {
			t.Errorf("%s reached a key the document never named", capability)
		} else if !strings.Contains(err.Error(), "env") {
			t.Errorf("%s: the refusal does not name the permission: %v", capability, err)
		}
	}
}

// Writing one key leaves the rest of the file alone, comments included: a .env
// is a file a person edits, and a station that reformatted it on every write
// would be one nobody could keep an ordering in.
func TestEnvWriteReplacesOneLineAndNothingElse(t *testing.T) {
	c, _ := brokerFor(t, station.Permissions{Env: station.Env{
		Read: []string{"MINECRAFT_VERSION"}, Write: []string{"MINECRAFT_VERSION"},
	}}, "# the stack's own\nMINECRAFT_VERSION=1.20.1\nRCON_PASSWORD=hunter2\n")

	if _, err := ask(t, c, "env.set", map[string]any{"key": "MINECRAFT_VERSION", "value": "1.21"}); err != nil {
		t.Fatalf("writing a declared key: %v", err)
	}

	app, err := db.GetApp(c.srv.db, c.srv.keyring, c.app.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := "# the stack's own\nMINECRAFT_VERSION=1.21\nRCON_PASSWORD=hunter2\n"
	if app.EnvContent != want {
		t.Errorf("the env is now %q, want %q", app.EnvContent, want)
	}
	if entries, _ := db.ListAudit(c.srv.db, "station.env.write", 10); len(entries) != 1 {
		t.Errorf("%d audit entries for an env write", len(entries))
	}
}

// A value carrying a newline would write a second line into the file, which is
// how one permitted key becomes two.
func TestAnEnvValueCannotCarryALineBreak(t *testing.T) {
	c, _ := brokerFor(t, station.Permissions{Env: station.Env{Write: []string{"MOTD"}}}, "MOTD=hi\n")

	_, err := ask(t, c, "env.set", map[string]any{"key": "MOTD", "value": "hi\nADMIN_TOKEN=stolen"})
	if err == nil {
		t.Fatal("a value with a line break was written")
	}
}

// The store needs no permission because it reaches nothing: one application,
// one station, and a cap so it stays the scratch space it is.
func TestTheStoreRoundTripsAndIsScoped(t *testing.T) {
	c, _ := brokerFor(t, station.Permissions{}, "")

	if _, err := ask(t, c, "store.set", map[string]any{"key": "updates", "value": map[string]any{"a": 1}}); err != nil {
		t.Fatalf("setting: %v", err)
	}
	got, err := ask(t, c, "store.get", map[string]any{"key": "updates"})
	if err != nil || string(got) != `{"a":1}` {
		t.Fatalf("got %s (%v)", got, err)
	}
	if missing, _ := ask(t, c, "store.get", map[string]any{"key": "never"}); string(missing) != "null" {
		t.Errorf("a key nobody set read back as %s", missing)
	}

	keys, _ := ask(t, c, "store.keys", nil)
	if string(keys) != `["updates"]` {
		t.Errorf("keys = %s", keys)
	}

	// Another station on the same application sees none of it.
	other := *c
	other.doc = station.Station{ID: "someone-else"}
	if got, _ := ask(t, &other, "store.get", map[string]any{"key": "updates"}); string(got) != "null" {
		t.Errorf("another station read %s out of this one's store", got)
	}

	if _, err := ask(t, c, "store.delete", map[string]any{"key": "updates"}); err != nil {
		t.Fatalf("deleting: %v", err)
	}
	if got, _ := ask(t, c, "store.get", map[string]any{"key": "updates"}); string(got) != "null" {
		t.Errorf("the key survived a delete: %s", got)
	}
}

// A capability that does not exist is refused rather than ignored, so a script
// written against a later version of Quasar fails where it can be read.
func TestAnUnknownCapabilityIsRefused(t *testing.T) {
	c, _ := brokerFor(t, station.Permissions{}, "")
	if _, err := ask(t, c, "docker.socket", nil); err == nil {
		t.Error("an invented capability was performed")
	}
}

// A series crosses the boundary the way everything else does, and needs no
// permission for the reason the store needs none: it is scoped to this
// application and this station and reaches nothing else. What it does need is
// an application — there is nothing to have measured before one exists — and
// the install form is the call that has none.
func TestSeriesRecordAndReadBack(t *testing.T) {
	c, _ := brokerFor(t, station.Permissions{}, "")

	for _, v := range []float64{4, 7} {
		if _, err := ask(t, c, "series.record", map[string]any{"name": "players", "value": v}); err != nil {
			t.Fatalf("recording: %v", err)
		}
	}

	read, err := ask(t, c, "series.read", map[string]any{"name": "players"})
	if err != nil {
		t.Fatalf("reading: %v", err)
	}
	var points []struct {
		At    string  `json:"at"`
		Value float64 `json:"value"`
	}
	if err := json.Unmarshal(read, &points); err != nil {
		t.Fatal(err)
	}
	if len(points) != 2 || points[0].Value != 4 || points[1].Value != 7 {
		t.Errorf("read back %s, want two samples of 4 then 7", read)
	}
	// The time is what new Date() takes, because a script that plots this is
	// going to want one.
	if _, err := time.Parse(time.RFC3339, points[0].At); err != nil {
		t.Errorf("the sample time %q is not a date a script can parse: %v", points[0].At, err)
	}

	names, err := ask(t, c, "series.names", map[string]any{})
	if err != nil || string(names) != `["players"]` {
		t.Errorf("names = %s (%v)", names, err)
	}

	// And a name the document mistyped comes back as a refusal an author can
	// read, rather than as a ninth empty chart.
	if _, err := ask(t, c, "series.record", map[string]any{"name": "Players Online", "value": 1}); err == nil {
		t.Error("a series name with spaces was accepted")
	}
}

// Filling in an install form runs before there is an application, so a station
// that samples from an action reachable both ways should read why rather than
// find a nil dereference.
func TestSeriesAreRefusedWithNoApplication(t *testing.T) {
	c, _ := brokerFor(t, station.Permissions{}, "")
	c.app = nil

	if _, err := ask(t, c, "series.record", map[string]any{"name": "players", "value": 1}); err == nil {
		t.Error("a sample was recorded with no application to record it about")
	} else if !strings.Contains(err.Error(), "install form") {
		t.Errorf("the refusal does not say why: %v", err)
	}
}
