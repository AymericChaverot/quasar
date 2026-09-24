package docker

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// The system stack — Traefik, the socket proxy and the dashboard — is defined
// by /opt/quasar/docker-compose.yml, and only an operator running `docker
// compose up -d` on the host ever applies that file. The dashboard's own
// update swaps its image and nothing else, and it cannot recreate the socket
// proxy it talks to the daemon through. So a server installed a while ago runs
// the stack as it was described then, and every feature since that needed a
// new mount or a new permission quietly does not work there.
//
// StackDrift is how the dashboard finds out: it compares the containers that
// are running with the compose file this version shipped with, and names what
// they lack.

// runningService is what StackDrift reads off a running container.
type runningService struct {
	Image  string
	Env    []string
	Mounts []runningMount
}

type runningMount struct {
	Source, Destination string
	RW                  bool
}

// stackService is the part of a system stack service StackDrift checks.
type stackService struct {
	Image         string   `yaml:"image"`
	ContainerName string   `yaml:"container_name"`
	Environment   stackEnv `yaml:"environment"`
	Volumes       []string `yaml:"volumes"`
}

// stackEnv reads either form compose accepts for environment: a mapping, or
// a list of KEY=value.
type stackEnv map[string]string

func (e *stackEnv) UnmarshalYAML(n *yaml.Node) error {
	*e = stackEnv{}
	switch n.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			(*e)[n.Content[i].Value] = n.Content[i+1].Value
		}
	case yaml.SequenceNode:
		for _, item := range n.Content {
			k, v, _ := strings.Cut(item.Value, "=")
			(*e)[k] = v
		}
	}
	return nil
}

// StackDrift lists what the running system containers lack compared with
// compose, the docker-compose.yml this version shipped with. installDir is
// where that file lives on the host, which relative paths in it are resolved
// against.
//
// Nothing is reported for a container that is not there at all — a
// development machine runs none of them — and nothing is an error: a check
// that cannot be made is a check not worth alarming anyone over.
//
// The three containers are inspected at once, since the System page waits on
// the answer: one round trip over the socket proxy rather than three.
func (c *Client) StackDrift(ctx context.Context, compose []byte, installDir string) StackState {
	names := []string{"quasar-socket-proxy", "quasar-traefik", "quasar-dashboard"}
	found := make([]*runningService, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			info, err := c.api.ContainerInspect(ctx, name)
			if err != nil || info.Config == nil {
				return
			}
			svc := runningService{Image: info.Config.Image, Env: info.Config.Env}
			for _, m := range info.Mounts {
				svc.Mounts = append(svc.Mounts, runningMount{Source: m.Source, Destination: m.Destination, RW: m.RW})
			}
			found[i] = &svc
		}()
	}
	wg.Wait()
	running := map[string]runningService{}
	seen := map[string]bool{}
	for i, svc := range found {
		if svc != nil {
			running[names[i]] = *svc
			seen[names[i]] = true
		}
	}
	return StackState{Drift: stackDrift(compose, installDir, running), Seen: seen}
}

// StackState is what StackDrift found.
type StackState struct {
	// Drift says, a line each, what the running containers lack.
	Drift []string
	// Seen names the containers that could be read. A container with no line
	// in Drift is only known to be up to date if it is here too: one that did
	// not answer in time has no line either.
	Seen map[string]bool
}

// UpToDate reports whether the container was read and lacks nothing.
func (s StackState) UpToDate(container string) bool {
	return s.Seen[container] && !slices.ContainsFunc(s.Drift, func(line string) bool {
		return strings.HasPrefix(line, container+" ")
	})
}

// stackDrift is StackDrift on containers already read, keyed by name.
//
// Only what is missing counts. A mount or a variable the operator added by
// hand is theirs; a value that is still interpolated from .env (${...}) says
// nothing about the file's age; and Traefik's image is left out, because the
// dashboard pins that one itself, through docker-compose.override.yml.
func stackDrift(compose []byte, installDir string, running map[string]runningService) []string {
	var file struct {
		Services map[string]stackService `yaml:"services"`
	}
	if err := yaml.Unmarshal(compose, &file); err != nil {
		return nil
	}
	names := make([]string, 0, len(file.Services))
	for name := range file.Services {
		names = append(names, name)
	}
	sort.Strings(names)

	var out []string
	for _, name := range names {
		want := file.Services[name]
		have, ok := running[want.ContainerName]
		if want.ContainerName == "" || !ok {
			continue
		}
		label := want.ContainerName
		if name != "traefik" && want.Image != "" && !strings.Contains(want.Image, "${") && have.Image != want.Image {
			out = append(out, fmt.Sprintf("%s runs %s; this version expects %s", label, have.Image, want.Image))
		}
		keys := make([]string, 0, len(want.Environment))
		for k := range want.Environment {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			v := want.Environment[k]
			if strings.Contains(v, "${") || slices.Contains(have.Env, k+"="+v) {
				continue
			}
			out = append(out, fmt.Sprintf("%s is not given %s=%s", label, k, v))
		}
		for _, vol := range want.Volumes {
			if problem := missingMount(vol, installDir, have.Mounts); problem != "" {
				out = append(out, label+" "+problem)
			}
		}
	}
	return out
}

// missingMount says how a compose volume entry is absent from a container's
// mounts, or returns "" when it is there as asked. Named volumes are skipped:
// the check is about the host paths the dashboard and Traefik rely on.
func missingMount(entry, installDir string, mounts []runningMount) string {
	parts := strings.Split(entry, ":")
	if len(parts) < 2 {
		return ""
	}
	src, dst := parts[0], parts[1]
	readOnly := len(parts) > 2 && strings.Contains(parts[2], "ro")
	switch {
	case strings.HasPrefix(src, "./"), src == ".":
		src = filepath.ToSlash(filepath.Join(installDir, src))
	case !strings.HasPrefix(src, "/"):
		return ""
	}
	for _, m := range mounts {
		if m.Destination != dst {
			continue
		}
		if !readOnly && !m.RW {
			return "mounts " + dst + " read-only; this version needs it read-write"
		}
		return ""
	}
	if readOnly {
		return "does not mount " + src + " (read-only) at " + dst
	}
	return "does not mount " + src + " at " + dst
}

// TraefikConfigCarriesEmail reports whether onDisk is the shipped traefik.yml
// with email written in place of its placeholder — what setup.sh used to do
// at install, and nothing else. Such a file is safe to restore once Traefik
// fills the email in itself; a file edited in any other way is the operator's,
// and is left out of it.
func TraefikConfigCarriesEmail(onDisk, shipped []byte, email string) bool {
	if email == "" || bytes.Equal(onDisk, shipped) {
		return false
	}
	return bytes.Equal(onDisk, bytes.ReplaceAll(shipped, []byte("{{ACME_EMAIL}}"), []byte(email)))
}
