package docker

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"quasar/internal/db"
)

// laptopStack is the shape a compose file has when it was written to run on its
// own: an nginx of its own on port 80, an API published beside it, and two
// services only that nginx ever talks to.
const laptopStack = `services:
  # Nginx reverse proxy — entry point on port 80
  nginx:
    image: nginx:alpine
    ports:
      - "80:80"
    volumes:
      - ./nginx/nginx.conf:/etc/nginx/nginx.conf:ro
    depends_on:
      - backend
      - frontend
    restart: unless-stopped

  backend:
    build: ./backend
    volumes:
      - ./data:/app/data
    env_file: .env
    ports:
      - "8080:8080"
    restart: unless-stopped

  frontend:
    build: ./frontend
    expose:
      - "80"
    restart: unless-stopped
`

func testOptions(choice string, port int) adaptOptions {
	return adaptOptions{
		Network: "traefik-net",
		Choice:  choice,
		Port:    port,
		Labels: func(p int) map[string]string {
			return map[string]string{
				"traefik.enable":                                             "true",
				"traefik.http.routers.qs-abcd1234.rule":                      "Host(`games.example.com`)",
				"traefik.http.services.qs-abcd1234.loadbalancer.server.port": strconv.Itoa(p),
			}
		},
	}
}

// adaptedModel is the generated file read back, which is what the assertions
// are made against: the point is the stack compose ends up with, not the exact
// bytes it is spelled with.
type adaptedModel struct {
	Services map[string]struct {
		Ports    []string          `yaml:"ports"`
		Expose   []string          `yaml:"expose"`
		Labels   map[string]string `yaml:"labels"`
		Networks []string          `yaml:"networks"`
	} `yaml:"services"`
	Networks map[string]struct {
		External bool `yaml:"external"`
	} `yaml:"networks"`
}

func mustAdapt(t *testing.T, src string, opt adaptOptions) (adaptedModel, ComposeAdaptation) {
	t.Helper()
	out, rep, err := adaptCompose([]byte(src), opt)
	if err != nil {
		t.Fatalf("adapt: %v", err)
	}
	if out == nil {
		t.Fatalf("nothing was adapted: %+v", rep)
	}
	var model adaptedModel
	if err := yaml.Unmarshal(out, &model); err != nil {
		t.Fatalf("the generated file is not valid yaml: %v\n%s", err, out)
	}
	return model, rep
}

func TestAdaptComposeRoutesTheServiceThatHeldTheHostPort(t *testing.T) {
	_, rep := mustAdapt(t, laptopStack, testOptions("", 80))

	if rep.Service != "nginx" {
		t.Fatalf("routed service = %q, want nginx", rep.Service)
	}
	if rep.Port != 80 {
		t.Errorf("routed port = %d, want 80", rep.Port)
	}
	if got := strings.Join(rep.Unpublished, ", "); got != "nginx 80:80" {
		t.Errorf("unpublished = %q, want \"nginx 80:80\"", got)
	}
	// The API's own binding is left alone but reported: it is a way past
	// Traefik, and only its author can say whether that is wanted.
	if got := strings.Join(rep.Published, ", "); got != "backend 8080:8080" {
		t.Errorf("published = %q, want \"backend 8080:8080\"", got)
	}
	if got := strings.Join(rep.Services, ","); got != "nginx,backend,frontend" {
		t.Errorf("services = %q, want them in the order written", got)
	}
}

func TestAdaptComposeMakesTheStackReachable(t *testing.T) {
	model, _ := mustAdapt(t, laptopStack, testOptions("", 80))

	nginx := model.Services["nginx"]
	if len(nginx.Ports) != 0 {
		t.Errorf("nginx still publishes %v, which is the bind Traefik already holds", nginx.Ports)
	}
	if len(nginx.Expose) != 1 || nginx.Expose[0] != "80" {
		t.Errorf("nginx expose = %v, want the container port it lost", nginx.Expose)
	}
	if nginx.Labels["traefik.enable"] != "true" {
		t.Errorf("nginx labels = %v, want the Traefik router", nginx.Labels)
	}
	if got := nginx.Labels["traefik.http.services.qs-abcd1234.loadbalancer.server.port"]; got != "80" {
		t.Errorf("routed port label = %q, want 80", got)
	}
	// Naming one network opts a service out of the default one, so the stack's
	// own services have to stay named alongside Traefik's.
	if got := strings.Join(nginx.Networks, ","); got != "default,traefik-net" {
		t.Errorf("nginx networks = %q, want default,traefik-net", got)
	}
	if !model.Networks["traefik-net"].External {
		t.Errorf("traefik-net = %+v, want it declared external", model.Networks["traefik-net"])
	}

	// Everything else is untouched, including the binding Quasar has no
	// business removing.
	if got := strings.Join(model.Services["backend"].Ports, ","); got != "8080:8080" {
		t.Errorf("backend ports = %q, want the author's own binding kept", got)
	}
	if len(model.Services["frontend"].Labels) != 0 {
		t.Errorf("frontend got labels %v, only the routed service should", model.Services["frontend"].Labels)
	}
}

func TestAdaptComposeLeavesAnAuthoredFileAlone(t *testing.T) {
	src := laptopStack + `
    labels:
      traefik.enable: "true"
`
	// Appending to the last service in the file: frontend now carries labels of
	// its author's own making.
	out, rep, err := adaptCompose([]byte(src), testOptions("", 80))
	if err != nil {
		t.Fatalf("adapt: %v", err)
	}
	if out != nil {
		t.Errorf("the file was rewritten over its author's own Traefik labels")
	}
	if !rep.Author || rep.Service != "frontend" {
		t.Errorf("report = %+v, want the authored service named", rep)
	}
	if rep.Adapted() {
		t.Errorf("Adapted() is true for a file nothing was done to")
	}
}

func TestAdaptComposeRefusesToGuessBetweenServices(t *testing.T) {
	src := `services:
  api:
    image: api
    expose: ["3000"]
  worker:
    image: worker
    expose: ["3001"]
`
	out, rep, err := adaptCompose([]byte(src), testOptions("", 80))
	if err != nil {
		t.Fatalf("adapt: %v", err)
	}
	if out != nil || !rep.Ambiguous {
		t.Fatalf("report = %+v, want nothing changed and the ambiguity reported", rep)
	}

	// Named explicitly, the same file adapts — and Traefik follows the service
	// rather than the port the app was configured with, since nothing listens
	// on 80 in there.
	model, rep := mustAdapt(t, src, testOptions("worker", 80))
	if rep.Service != "worker" || rep.Port != 3001 {
		t.Fatalf("routed %s:%d, want worker:3001", rep.Service, rep.Port)
	}
	if got := model.Services["worker"].Labels["traefik.http.services.qs-abcd1234.loadbalancer.server.port"]; got != "3001" {
		t.Errorf("routed port label = %q, want 3001", got)
	}
}

// Plenty of compose files publish nothing at all — written for a server from
// the start, or run through `docker compose exec`. Nothing about a port is
// available to go on, so the shape of the stack has to be enough.
func TestAdaptComposeFollowsTheShapeOfTheStack(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string // the service that must be routed, "" for undecidable
	}{
		{
			// Nothing names a port, and no image is recognisable: only
			// depends_on says which way round the stack goes.
			name: "the service everything else is behind",
			src: `services:
  caddy:
    image: caddy:2
    depends_on: [api, assets]
  api:
    build: ./api
  assets:
    build: ./assets
`,
			want: "caddy",
		},
		{
			// The long spelling, which a stack waiting on a healthcheck uses.
			name: "depends_on written as conditions",
			src: `services:
  app:
    build: .
    depends_on:
      db:
        condition: service_healthy
  db:
    image: postgres:16
`,
			want: "app",
		},
		{
			// The guard: worker/queue form a graph, but web stands outside it,
			// so the graph does not describe this stack and handing the domain
			// to the worker would be a guess dressed up as a deduction.
			name: "a service standing outside the graph",
			src: `services:
  web:
    build: ./web
  worker:
    build: ./worker
    depends_on: [queue]
  queue:
    image: redis
`,
			want: "",
		},
		{
			name: "two services both fronting something",
			src: `services:
  web:
    build: ./web
    depends_on: [db]
  admin:
    build: ./admin
    depends_on: [db]
  db:
    image: postgres:16
`,
			want: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, rep, err := adaptCompose([]byte(tc.src), testOptions("", 0))
			if err != nil {
				t.Fatal(err)
			}
			if rep.Service != tc.want {
				t.Errorf("routed %q, want %q", rep.Service, tc.want)
			}
			if tc.want == "" && !rep.Ambiguous {
				t.Errorf("report = %+v, want the ambiguity reported rather than a guess", rep)
			}
		})
	}
}

// Anchors and merge keys are ordinary compose. Read without expanding them a
// service looks like it declares nothing; written into without detaching them
// the labels land on the anchor, and every service sharing it claims the
// domain.
func TestAdaptComposeSeesThroughAnchors(t *testing.T) {
	src := `x-service: &defaults
  restart: unless-stopped
  networks: [internal]

services:
  edge:
    <<: *defaults
    image: caddy:2
    ports:
      - "80:80"
  api:
    <<: *defaults
    build: ./api
    expose: ["9000"]

networks:
  internal:
`
	model, rep := mustAdapt(t, src, testOptions("", 80))
	if rep.Service != "edge" {
		t.Fatalf("routed %q, want edge", rep.Service)
	}
	if got := strings.Join(rep.Unpublished, ", "); got != "edge 80:80" {
		t.Errorf("unpublished = %q, want the binding it inherited nothing about", got)
	}

	edge, api := model.Services["edge"], model.Services["api"]
	// The merged keys survive the flattening: dropping them would strip the
	// restart policy and the network off every service in the file.
	if got := strings.Join(edge.Networks, ","); got != "internal,traefik-net" {
		t.Errorf("edge networks = %q, want internal,traefik-net", got)
	}
	if got := strings.Join(api.Networks, ","); got != "internal" {
		t.Errorf("api networks = %q, want the merged internal alone", got)
	}
	if len(api.Labels) != 0 {
		t.Errorf("api carries %v — the labels leaked through the shared anchor", api.Labels)
	}
	if edge.Labels["traefik.enable"] != "true" {
		t.Errorf("edge labels = %v, want the Traefik router", edge.Labels)
	}
}

// A service written as an alias of another is one service, not a view of one:
// what is written into it must not reach the anchor.
func TestAdaptComposeDetachesAnAliasedService(t *testing.T) {
	src := `x-app: &app
  image: app:1
  expose: ["3000"]

services:
  web: *app
  worker:
    <<: *app
    command: work
`
	model, rep := mustAdapt(t, src, testOptions("web", 3000))
	if rep.Service != "web" || rep.Port != 3000 {
		t.Fatalf("routed %s:%d, want web:3000", rep.Service, rep.Port)
	}
	if len(model.Services["worker"].Labels) != 0 {
		t.Errorf("worker carries %v — writing to the alias reached the anchor", model.Services["worker"].Labels)
	}
	if got := strings.Join(model.Services["worker"].Expose, ","); got != "3000" {
		t.Errorf("worker expose = %q, want what it merged in", got)
	}
}

func TestAdaptComposeReportsAServiceThatIsGone(t *testing.T) {
	out, rep, err := adaptCompose([]byte(laptopStack), testOptions("renamed", 80))
	if err != nil {
		t.Fatalf("adapt: %v", err)
	}
	if out != nil || !rep.Gone {
		t.Fatalf("report = %+v, want nothing changed and the missing service reported", rep)
	}
}

func TestAdaptComposePicksTheOnlyServiceThereIs(t *testing.T) {
	src := `services:
  app:
    image: app
    ports: ["3000:3000"]
`
	_, rep := mustAdapt(t, src, testOptions("", 3000))
	if rep.Service != "app" || rep.Port != 3000 {
		t.Fatalf("routed %s:%d, want app:3000", rep.Service, rep.Port)
	}
	if len(rep.Unpublished) != 0 {
		t.Errorf("unpublished %v, a binding that does not collide is not Quasar's to remove", rep.Unpublished)
	}
}

func TestAdaptComposeHandlesTheLongPortSyntax(t *testing.T) {
	src := `services:
  web:
    image: web
    ports:
      - target: 8000
        published: "443"
        protocol: tcp
  db:
    image: postgres
`
	model, rep := mustAdapt(t, src, testOptions("", 80))
	if rep.Service != "web" || rep.Port != 8000 {
		t.Fatalf("routed %s:%d, want web:8000", rep.Service, rep.Port)
	}
	if len(model.Services["web"].Ports) != 0 {
		t.Errorf("web still publishes %v", model.Services["web"].Ports)
	}
	if got := strings.Join(rep.Unpublished, ", "); got != "web 443:8000" {
		t.Errorf("unpublished = %q, want \"web 443:8000\"", got)
	}
}

// A repository whose compose file already puts its front end on a network of
// its own must keep it: dropping it would cut the service off from the stack.
func TestAdaptComposeKeepsTheNetworksAlreadyDeclared(t *testing.T) {
	src := `services:
  web:
    image: web
    ports: ["80:80"]
    networks: [internal]
networks:
  internal:
`
	model, _ := mustAdapt(t, src, testOptions("", 80))
	if got := strings.Join(model.Services["web"].Networks, ","); got != "internal,traefik-net" {
		t.Errorf("web networks = %q, want internal,traefik-net", got)
	}
	if !model.Networks["traefik-net"].External {
		t.Errorf("traefik-net is not declared external")
	}
	if _, ok := model.Networks["internal"]; !ok {
		t.Errorf("the stack's own network declaration was dropped")
	}
}

// The generated file is what compose is actually run with, and what it is
// generated from stays exactly as its author wrote it.
func TestWriteAdaptedComposeLeavesTheRepositoryAlone(t *testing.T) {
	c := &Client{appsDir: t.TempDir(), network: "traefik-net", domain: "example.com"}
	a := &db.App{ID: "a1", Name: "Games", Subdomain: "games", DeployType: "git", Port: 80}
	src := c.sourceDir(a.ID)
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	original := filepath.Join(src, "docker-compose.yml")
	if err := os.WriteFile(original, []byte(laptopStack), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := c.writeAdaptedCompose(a); err != nil {
		t.Fatalf("write: %v", err)
	}
	if kept, _ := os.ReadFile(original); string(kept) != laptopStack {
		t.Errorf("the repository's own compose file was modified:\n%s", kept)
	}

	adapted := filepath.Join(src, adaptedComposeFile)
	body, err := os.ReadFile(adapted)
	if err != nil {
		t.Fatalf("the adapted file was not written: %v", err)
	}
	if !strings.Contains(string(body), "Generated by Quasar") {
		t.Errorf("the generated file does not say it is generated:\n%s", body)
	}
	if !strings.Contains(string(body), "Host(`games.example.com`)") {
		t.Errorf("the generated file carries no router rule for the app:\n%s", body)
	}
	// Every compose command has to act on the stack that is really running.
	if _, file := c.composeContext(a); file != adapted {
		t.Errorf("composeContext = %q, want the adapted file %q", file, adapted)
	}

	// A file that stops being adaptable takes its generated leftover with it:
	// compose would otherwise keep running a stack made from a file that no
	// longer says what it says.
	if err := os.WriteFile(original, []byte("services:\n  a: {image: a}\n  b: {image: b}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := c.writeAdaptedCompose(a); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := os.Stat(adapted); !os.IsNotExist(err) {
		t.Errorf("the generated file outlived the file it was generated from")
	}
	if _, file := c.composeContext(a); file != original {
		t.Errorf("composeContext = %q, want the repository's own file", file)
	}
}

// Two deploys of an unchanged file must produce the same bytes, or every
// redeploy would look like a change to whoever reads the generated file.
func TestAdaptComposeIsStable(t *testing.T) {
	first, _, err := adaptCompose([]byte(laptopStack), testOptions("", 80))
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := adaptCompose([]byte(laptopStack), testOptions("", 80))
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Errorf("adapting twice gave different files:\n%s\n---\n%s", first, second)
	}
}

// The reported bug: a stack behind basic auth kept asking for credentials that
// were correct. Compose interpolates the file it is handed, so the "$KI2SuB"
// segment of the bcrypt hash was read as an undefined variable and dropped —
// Traefik was left comparing passwords against a truncated hash, which nothing
// matches. Both label spellings are checked: the mapping Quasar writes for a
// service with no labels, and the sequence it appends to when the author used
// the list form.
func TestAdaptComposeEscapesDollarsInGeneratedLabels(t *testing.T) {
	const hash = "ops:$2a$10$KI2SuB.6c4e0JVLXdPK1leU2X8Rii7DjWWCKCBZiYbIWWa96E8MRe"
	opt := testOptions("", 80)
	opt.Labels = func(p int) map[string]string {
		return map[string]string{
			"traefik.enable": "true",
			"traefik.http.middlewares.qs-abcd1234-auth.basicauth.users": hash,
		}
	}

	mapping := `services:
  web:
    image: nginx
    ports: ["80:80"]
`
	sequence := `services:
  web:
    image: nginx
    ports: ["80:80"]
    labels:
      - "com.example.owner=ops"
`
	for _, src := range []string{mapping, sequence} {
		out, _, err := adaptCompose([]byte(src), opt)
		if err != nil {
			t.Fatalf("adapt: %v", err)
		}
		written := string(out)
		if !strings.Contains(written, strings.ReplaceAll(hash, "$", "$$")) {
			t.Errorf("the hash is written unescaped, so compose will eat part of it:\n%s", written)
		}
		// What compose does with the file: "$$" collapses back to one "$",
		// which has to leave the hash exactly as it was stored.
		if got := strings.ReplaceAll(written, "$$", "$"); !strings.Contains(got, hash) {
			t.Errorf("after compose interpolation the hash is not itself:\n%s", got)
		}
	}
}
