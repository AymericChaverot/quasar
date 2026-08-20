package catalog

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// A smoke test that actually deploys the catalogue. Reading a compose file
// proves it parses; only running it proves the image still takes the
// environment variables the entry sets, that the service listens on the port
// the entry routes to, and that the stack's parts can see each other.
//
// It pulls real images, so it is off by default:
//
//	CATALOG_DEPLOY=1 go test ./internal/catalog/ -run TestDeploy -timeout 2h
//	CATALOG_DEPLOY=1 CATALOG_ONLY=immich,outline go test ./internal/catalog/ -run TestDeploy -v
//
// CATALOG_KEEP=1 leaves the projects up for inspection instead of tearing them
// down, and CATALOG_PRUNE=1 removes each stack's images afterwards, which keeps
// a full run inside a few GB of disk at the cost of re-pulling next time.

// probeHost stands in for the public hostname Quasar would fill in. It is a
// real name rather than 127.0.0.1 because several of these validate it, and
// the probe reaches the container by published port regardless of what the app
// believes it is called.
const probeHost = "app.example.com"

func TestDeployEveryTemplate(t *testing.T) {
	if os.Getenv("CATALOG_DEPLOY") == "" {
		t.Skip("set CATALOG_DEPLOY=1 to deploy the catalogue for real")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not on PATH")
	}

	only := map[string]bool{}
	for _, id := range strings.Split(os.Getenv("CATALOG_ONLY"), ",") {
		if id = strings.TrimSpace(id); id != "" {
			only[id] = true
		}
	}

	for _, e := range Templates {
		if len(only) > 0 && !only[e.ID] {
			continue
		}
		t.Run(e.ID, func(t *testing.T) {
			t.Parallel()
			if e.NeedsSetup != "" {
				// Not a pass being hidden: the entry says on its own card that
				// it cannot start without credentials for something outside
				// this server, and there is nothing to invent for it here.
				t.Skipf("needs operator setup: %s", e.NeedsSetup)
			}
			deployOne(t, e)
		})
	}
}

// deployOne brings one entry up in its own compose project, waits for it to
// answer, and tears it down again.
func deployOne(t *testing.T, e Template) {
	t.Helper()

	dir := t.TempDir()
	port, err := freePort()
	if err != nil {
		t.Fatalf("no free port: %v", err)
	}

	compose, err := probeCompose(e, port)
	if err != nil {
		t.Fatalf("building the test compose file: %v", err)
	}
	write(t, filepath.Join(dir, "docker-compose.yml"), compose)
	write(t, filepath.Join(dir, ".env"), e.RenderEnv(probeHost, e.Resolve(nil))+"\n")
	// The same directory Quasar creates before a first deploy.
	if err := os.MkdirAll(filepath.Join(dir, "data"), 0o777); err != nil {
		t.Fatal(err)
	}

	project := "catalogtest-" + e.ID
	defer func() {
		if os.Getenv("CATALOG_KEEP") != "" {
			t.Logf("left %s up on port %d", project, port)
			return
		}
		args := []string{"down", "-v", "--remove-orphans", "-t", "5"}
		if os.Getenv("CATALOG_PRUNE") != "" {
			args = append(args, "--rmi", "all")
		}
		_, _ = compose_(dir, project, 10*time.Minute, args...)
	}()

	// Pull first and separately: on a cold cache this is nearly all of the
	// wall time, and folding it into `up` makes a slow pull look like a
	// failure to start.
	//
	// Retried because several pulls at once make containerd drop a blob it was
	// halfway through writing ("failed commit on ref ... no such file or
	// directory"). That says nothing about the entry, and letting it through
	// as a failure would train everyone to ignore this test.
	var pullOut string
	var pullErr error
	for attempt := 1; attempt <= 3; attempt++ {
		pullOut, pullErr = compose_(dir, project, 45*time.Minute, "pull", "-q")
		if pullErr == nil {
			break
		}
		t.Logf("pull attempt %d failed, retrying: %v", attempt, pullErr)
		time.Sleep(time.Duration(attempt) * 10 * time.Second)
	}
	if pullErr != nil {
		t.Fatalf("pull failed after 3 attempts: %v\n%s", pullErr, tail(pullOut))
	}
	if out, err := compose_(dir, project, 10*time.Minute, "up", "-d"); err != nil {
		t.Fatalf("up failed: %v\n%s", err, tail(out))
	}

	if err := waitReady(e, dir, project, port); err != nil {
		out, _ := compose_(dir, project, 2*time.Minute, "logs", "--tail", "40")
		ps, _ := compose_(dir, project, time.Minute, "ps", "-a")
		t.Fatalf("%v\n--- ps ---\n%s\n--- logs ---\n%s", err, ps, tail(out))
	}
}

// waitReady blocks until the entry looks alive: an HTTP answer for a web app,
// a container that stays up for one that speaks its own protocol.
func waitReady(e Template, dir, project string, port int) error {
	// Every stack gets the same generous budget. It costs nothing on a healthy
	// entry — the loop returns the moment the app answers — and only lengthens
	// the path to a genuine failure. It is this wide because first boot is
	// where these are slowest and most machine-dependent: Nextcloud unpacks
	// its whole application into the mounted directory before it listens,
	// which takes seconds on a Linux server and the best part of ten minutes
	// over a Docker Desktop bind mount.
	deadline := time.Now().Add(20 * time.Minute)

	if e.Raw {
		// Nothing to ask a Minecraft server over HTTP. What can be checked is
		// that it holds: an image with a bad env var exits or restart-loops
		// within seconds, so a container still up after a settle is the real
		// signal here.
		time.Sleep(45 * time.Second)
		return assertUp(dir, project)
	}

	url := fmt.Sprintf("http://127.0.0.1:%d/", port)
	client := &http.Client{
		Timeout: 10 * time.Second,
		// Do not follow redirects. Several of these answer 302 to the public
		// URL the entry has not been given yet; chasing it leaves the
		// container entirely and fails on a host that was never the subject.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	var last error
	for time.Now().Before(deadline) {
		// A crashed container will never answer; fail early rather than
		// spending the whole budget waiting for something that is gone.
		if err := assertUp(dir, project); err != nil {
			return err
		}
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			// Any status is a pass. Several of these answer 302 to a login
			// page, 401, or even 400 on an unexpected Host header — all of
			// them prove the service is listening and serving.
			return nil
		}
		last = err
		time.Sleep(3 * time.Second)
	}
	return fmt.Errorf("nothing answered on %s within the budget: %w", url, last)
}

// assertUp fails if any container of the project has exited or is restarting.
func assertUp(dir, project string) error {
	out, err := compose_(dir, project, time.Minute, "ps", "-a", "--format", "{{.Service}} {{.State}} {{.ExitCode}}")
	if err != nil {
		return fmt.Errorf("compose ps: %w", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Fields(strings.TrimSpace(line))
		if len(f) < 2 {
			continue
		}
		switch f[1] {
		case "exited", "dead":
			// A one-shot init container that succeeded is not a failure.
			if len(f) > 2 && f[2] == "0" {
				continue
			}
			return fmt.Errorf("service %q exited (code %s)", f[0], strings.Join(f[2:], " "))
		case "restarting":
			return fmt.Errorf("service %q is restart-looping", f[0])
		}
	}
	return nil
}

// probeCompose is the entry's stack rewritten for the test: the routed service
// publishes its port on the host so the probe can reach it without Traefik.
// An image entry is wrapped in a one-service file so both kinds share a path.
func probeCompose(e Template, hostPort int) (string, error) {
	if e.Type() == "image" {
		svc := map[string]any{
			"image": e.ImageRef,
			"ports": []string{fmt.Sprintf("%d:%d", hostPort, e.Port)},
		}
		// Quasar binds apps/<id>/data at DataMount on every deploy, and some
		// images refuse to run without it — Vaultwarden exits rather than risk
		// silent data loss. Leaving it out would test something Quasar never
		// actually does.
		if e.DataMount != "" {
			svc["volumes"] = []string{"./data:" + e.DataMount}
		}
		if env := envPairs(e.RenderEnv(probeHost, e.Resolve(nil))); len(env) > 0 {
			svc["environment"] = env
		}
		doc := map[string]any{"services": map[string]any{"app": svc}}
		b, err := yaml.Marshal(doc)
		return string(b), err
	}

	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(e.Compose), &doc); err != nil {
		return "", err
	}
	services := mapEntry(mapEntry(doc.Content[0], "services"), e.routedService())
	if services == nil {
		return "", fmt.Errorf("service %q not found in the compose file", e.routedService())
	}
	// Replace any ports the entry declares: a game server's own published
	// port would collide when several of these run at once.
	setMapEntry(services, "ports", &yaml.Node{
		Kind:    yaml.SequenceNode,
		Content: []*yaml.Node{{Kind: yaml.ScalarNode, Value: fmt.Sprintf("%d:%d", hostPort, e.Port)}},
	})
	b, err := yaml.Marshal(&doc)
	return string(b), err
}

// routedService is the service the domain points at, or the only one there is.
func (t Template) routedService() string {
	if t.ComposeService != "" {
		return t.ComposeService
	}
	var doc struct {
		Services map[string]any `yaml:"services"`
	}
	if err := yaml.Unmarshal([]byte(t.Compose), &doc); err == nil && len(doc.Services) == 1 {
		for name := range doc.Services {
			return name
		}
	}
	return ""
}

func envPairs(env string) []string {
	var out []string
	for _, line := range strings.Split(env, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out
}

func compose_(dir, project string, timeout time.Duration, args ...string) (string, error) {
	full := append([]string{"compose", "-p", project, "--env-file", ".env"}, args...)
	cmd := exec.Command("docker", full...)
	cmd.Dir = dir
	done := make(chan struct{})
	var out []byte
	var err error
	go func() { out, err = cmd.CombinedOutput(); close(done) }()
	select {
	case <-done:
		return string(out), err
	case <-time.After(timeout):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		return string(out), fmt.Errorf("docker compose %s timed out after %s", args[0], timeout)
	}
}

func mapEntry(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

func setMapEntry(n *yaml.Node, key string, val *yaml.Node) {
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			n.Content[i+1] = val
			return
		}
	}
	n.Content = append(n.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: key}, val)
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func tail(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > 40 {
		lines = lines[len(lines)-40:]
	}
	return strings.Join(lines, "\n")
}
