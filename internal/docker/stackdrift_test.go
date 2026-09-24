package docker

import (
	"bytes"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"quasar"
)

// upToDate is the system stack as `docker compose up -d` would run it from
// the compose file this version ships.
func upToDate(t *testing.T) map[string]runningService {
	t.Helper()
	var file struct {
		Services map[string]stackService `yaml:"services"`
	}
	if err := yaml.Unmarshal(quasar.ComposeFile, &file); err != nil {
		t.Fatal(err)
	}
	out := map[string]runningService{}
	for _, svc := range file.Services {
		run := runningService{Image: svc.Image}
		for k, v := range svc.Environment {
			run.Env = append(run.Env, k+"="+v)
		}
		for _, vol := range svc.Volumes {
			parts := strings.Split(vol, ":")
			src := parts[0]
			if strings.HasPrefix(src, "./") {
				src = filepath.ToSlash(filepath.Join("/opt/quasar", src))
			}
			ro := len(parts) > 2 && strings.Contains(parts[2], "ro")
			run.Mounts = append(run.Mounts, runningMount{Source: src, Destination: parts[1], RW: !ro})
		}
		out[svc.ContainerName] = run
	}
	return out
}

func TestStackDriftNoneWhenUpToDate(t *testing.T) {
	if got := stackDrift(quasar.ComposeFile, "/opt/quasar", upToDate(t)); len(got) != 0 {
		t.Errorf("a stack run from this compose file reports drift: %q", got)
	}
}

// The case that started this: an install from before the certificate store
// was mounted read-write, on the socket proxy of the time.
func TestStackDriftNamesWhatAnOldInstallLacks(t *testing.T) {
	running := upToDate(t)
	dash := running["quasar-dashboard"]
	dash.Mounts = slices.DeleteFunc(dash.Mounts, func(m runningMount) bool { return m.Destination == "/opt/quasar/traefik" })
	running["quasar-dashboard"] = dash
	proxy := running["quasar-socket-proxy"]
	proxy.Image = "tecnativa/docker-socket-proxy:0.3.0"
	proxy.Env = slices.DeleteFunc(proxy.Env, func(e string) bool { return strings.HasPrefix(e, "SESSION=") })
	running["quasar-socket-proxy"] = proxy

	got := strings.Join(stackDrift(quasar.ComposeFile, "/opt/quasar", running), "\n")
	for _, want := range []string{
		"quasar-dashboard does not mount /opt/quasar/traefik at /opt/quasar/traefik",
		"quasar-socket-proxy runs tecnativa/docker-socket-proxy:0.3.0",
		"quasar-socket-proxy is not given SESSION=1",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("drift does not say %q; it says:\n%s", want, got)
		}
	}
}

func TestStackDriftReadOnlyWhereItShouldWrite(t *testing.T) {
	running := upToDate(t)
	dash := running["quasar-dashboard"]
	for i, m := range dash.Mounts {
		if m.Destination == "/opt/quasar/traefik" {
			dash.Mounts[i].RW = false
		}
	}
	got := stackDrift(quasar.ComposeFile, "/opt/quasar", running)
	if len(got) != 1 || !strings.Contains(got[0], "/opt/quasar/traefik read-only") {
		t.Errorf("drift = %q, want the read-only certificate store", got)
	}
}

// What an operator added on purpose, and what Quasar pins itself, is not a
// sign of an old install.
func TestStackDriftIgnoresAdditionsAndPins(t *testing.T) {
	running := upToDate(t)
	dash := running["quasar-dashboard"]
	dash.Mounts = append(dash.Mounts, runningMount{Source: "/var/lib/docker/volumes", Destination: "/var/lib/docker/volumes", RW: true})
	dash.Env = append(dash.Env, "EXTRA=1")
	dash.Image = "ghcr.io/aymericchaverot/quasar:v9.9.9"
	running["quasar-dashboard"] = dash
	traefik := running["quasar-traefik"]
	traefik.Image = "traefik:v9.0.0"
	running["quasar-traefik"] = traefik

	if got := stackDrift(quasar.ComposeFile, "/opt/quasar", running); len(got) != 0 {
		t.Errorf("additions and pins reported as drift: %q", got)
	}
}

// A development machine runs none of the system containers.
func TestStackDriftNoneWithoutContainers(t *testing.T) {
	if got := stackDrift(quasar.ComposeFile, "/opt/quasar", nil); len(got) != 0 {
		t.Errorf("no containers reported drift: %q", got)
	}
}

func TestTraefikConfigCarriesEmail(t *testing.T) {
	shipped := quasar.TraefikConfig
	filled := bytes.ReplaceAll(shipped, []byte("{{ACME_EMAIL}}"), []byte("ops@example.com"))
	edited := append(bytes.Clone(filled), []byte("\n# my own note\n")...)
	cases := []struct {
		name   string
		onDisk []byte
		email  string
		want   bool
	}{
		{"as setup.sh left it", filled, "ops@example.com", true},
		{"already restored", shipped, "ops@example.com", false},
		{"edited beyond the email", edited, "ops@example.com", false},
		{"another email than .env's", filled, "someone@example.com", false},
		{"no email to compare with", filled, "", false},
	}
	for _, c := range cases {
		if got := TraefikConfigCarriesEmail(c.onDisk, shipped, c.email); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// Up to date means read and lacking nothing; a container that did not answer
// is not known to be anything.
func TestStackStateUpToDate(t *testing.T) {
	s := StackState{
		Drift: []string{"quasar-dashboard does not mount /opt/quasar/traefik at /opt/quasar/traefik"},
		Seen:  map[string]bool{"quasar-dashboard": true, "quasar-traefik": true},
	}
	if !s.UpToDate("quasar-traefik") {
		t.Error("a container read with nothing missing is not up to date")
	}
	if s.UpToDate("quasar-dashboard") {
		t.Error("a container with drift is up to date")
	}
	if s.UpToDate("quasar-socket-proxy") {
		t.Error("a container that was not read is up to date")
	}
}
