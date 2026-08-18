package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"quasar/internal/db"
	"quasar/internal/docker"
	"quasar/internal/station"
)

// fakeDocker records what reached the containers half, which is what these
// tests are about: which commands get through, and in what shape. Whether
// Docker then runs them is Docker's business and not something worth a daemon
// in a unit test.
type fakeDocker struct {
	service string
	argv    []string
	stdin   string
	result  docker.ExecResult
	logs    string
	tail    int
}

func (f *fakeDocker) ExecInService(_ context.Context, _ *db.App, service string, argv []string, stdin string) (docker.ExecResult, error) {
	f.service, f.argv, f.stdin = service, argv, stdin
	return f.result, nil
}

func (f *fakeDocker) TailLogs(_ context.Context, _ *db.App, service string, tail int, _ string) (string, error) {
	f.service, f.tail = service, tail
	return f.logs, nil
}

// execFor is a station granted exec and logs on the services named.
func execFor(t *testing.T, perms station.Permissions) (*stationCall, *fakeDocker) {
	t.Helper()
	c, _ := brokerFor(t, perms, "")
	dock := &fakeDocker{result: docker.ExecResult{Code: 0, Stdout: "There are 3 of a max of 20 players online"}}
	c.dock = dock
	return c, dock
}

// The permission names services, and a station granted exec on the game server
// has not been granted it on the database beside it.
func TestExecIsRefusedForServicesTheDocumentDidNotName(t *testing.T) {
	c, dock := execFor(t, station.Permissions{Exec: station.Services{Services: []string{"minecraft"}}})

	if _, err := ask(t, c, "exec", map[string]any{"service": "minecraft", "argv": []string{"rcon-cli", "list"}}); err != nil {
		t.Fatalf("a declared service was refused: %v", err)
	}
	if dock.service != "minecraft" {
		t.Errorf("the command went to %q", dock.service)
	}

	for _, service := range []string{"db", "", "postgres"} {
		_, err := ask(t, c, "exec", map[string]any{"service": service, "argv": []string{"sh"}})
		if err == nil {
			t.Errorf("a command ran in %q, which the document never named", service)
		} else if !strings.Contains(err.Error(), "exec") {
			t.Errorf("%q: the refusal does not name the permission: %v", service, err)
		}
	}
}

// exec and logs are separate grants: reading why a server refused to start is
// not the same capability as being able to do anything at all inside it.
func TestLogsAndExecAreSeparatePermissions(t *testing.T) {
	c, dock := execFor(t, station.Permissions{Logs: station.Services{Services: []string{"minecraft"}}})
	dock.logs = "[Server] Done (12.4s)!"

	got, err := ask(t, c, "logs", map[string]any{"service": "minecraft", "tail": 50})
	if err != nil {
		t.Fatalf("a declared service's logs were refused: %v", err)
	}
	var text string
	json.Unmarshal(got, &text)
	if text != dock.logs || dock.tail != 50 {
		t.Errorf("logs = %q, tail = %d", text, dock.tail)
	}

	// The logs permission does not carry exec with it.
	if _, err := ask(t, c, "exec", map[string]any{"service": "minecraft", "argv": []string{"sh"}}); err == nil {
		t.Error("a station granted only logs ran a command")
	}
}

// argv, not a shell string. A station interpolates constantly, and a mod named
// "x; rm -rf /.jar" has to stay one awkward argument rather than becoming two
// commands.
func TestArgvIsNotShellInterpreted(t *testing.T) {
	c, dock := execFor(t, station.Permissions{Exec: station.Services{Services: []string{"minecraft"}}})

	nasty := "x; rm -rf /.jar"
	if _, err := ask(t, c, "exec", map[string]any{
		"service": "minecraft", "argv": []string{"rm", "/data/mods/" + nasty},
	}); err != nil {
		t.Fatal(err)
	}

	if len(dock.argv) != 2 {
		t.Fatalf("argv = %q, want two arguments", dock.argv)
	}
	if dock.argv[1] != "/data/mods/"+nasty {
		t.Errorf("argv[1] = %q, want the filename whole", dock.argv[1])
	}
}

// Output over the cap comes back truncated and says so, rather than arriving
// as half a file that reads like the whole one.
func TestTruncatedOutputSaysSo(t *testing.T) {
	c, dock := execFor(t, station.Permissions{Exec: station.Services{Services: []string{"minecraft"}}})
	dock.result = docker.ExecResult{Code: 1, Stdout: "…", Stderr: "boom", Truncated: true}

	got, err := ask(t, c, "exec", map[string]any{"service": "minecraft", "argv": []string{"cat", "/dev/urandom"}})
	if err != nil {
		t.Fatal(err)
	}
	var result docker.ExecResult
	if err := json.Unmarshal(got, &result); err != nil {
		t.Fatal(err)
	}
	if !result.Truncated {
		t.Error("the result does not say the output was cut")
	}
	// A non-zero exit is the script's to handle, not an error from the
	// capability: "the command failed" is often the answer an action wanted.
	if result.Code != 1 || result.Stderr != "boom" {
		t.Errorf("result = %+v", result)
	}
}

// Every command a station ran has an entry, whatever it returned: the audit
// log's question is what this station did, and a command that failed was still
// run.
func TestEveryCommandIsAudited(t *testing.T) {
	c, _ := execFor(t, station.Permissions{Exec: station.Services{Services: []string{"minecraft"}}})

	ask(t, c, "exec", map[string]any{"service": "minecraft", "argv": []string{"rcon-cli", "say hello"}})
	entries, _ := db.ListAudit(c.srv.db, "station.exec", 10)
	if len(entries) != 1 {
		t.Fatalf("%d audit entries for one command", len(entries))
	}
	if !strings.Contains(entries[0].Detail, "rcon-cli") || !strings.Contains(entries[0].Detail, "minecraft") {
		t.Errorf("the entry does not say what was run where: %q", entries[0].Detail)
	}
}
