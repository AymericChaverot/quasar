package docker

import (
	"strings"
	"testing"
)

func TestResolveVars(t *testing.T) {
	env := map[string]string{"HTTP_PORT": "80", "EMPTY": "", "HOST": "127.0.0.1"}

	tests := []struct {
		name string
		in   string
		want string
	}{
		{"no reference is left alone", "8080:80", "8080:80"},
		{"braced reference", "${HTTP_PORT}:80", "80:80"},
		{"bare reference", "$HTTP_PORT:80", "80:80"},
		{"default is used when unset", "${MISSING:-8080}:80", "8080:80"},
		{"default is ignored when set", "${HTTP_PORT:-8080}:80", "80:80"},
		{`":-" falls back on an empty value`, "${EMPTY:-8080}:80", "8080:80"},
		{`"-" keeps an empty value`, "${EMPTY-8080}:80", ":80"},
		{"unset and no default becomes empty", "${MISSING}:80", ":80"},
		{"required form takes the value it has", "${HTTP_PORT:?must be set}:80", "80:80"},
		{"escaped dollar stays a dollar", "$$HTTP_PORT:80", "$HTTP_PORT:80"},
		{"several references in one entry", "${HOST}:${HTTP_PORT}:80", "127.0.0.1:80:80"},
		{"an unterminated brace is not a reference", "${HTTP_PORT:80", "${HTTP_PORT:80"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveVars(tc.in, env); got != tc.want {
				t.Errorf("resolveVars(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestEnvMap(t *testing.T) {
	got := envMap("# a comment\nHTTP_PORT=80\nQUOTED=\"a value\"\n\nBARE\nPUBLIC_SITE_URL=https://x.example.com\n")
	want := map[string]string{
		"HTTP_PORT":       "80",
		"QUOTED":          "a value",
		"PUBLIC_SITE_URL": "https://x.example.com",
	}
	if len(got) != len(want) {
		t.Fatalf("envMap() = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("envMap()[%q] = %q, want %q", k, got[k], v)
		}
	}
}

// portfolioStack is the shape that sent this whole thing wrong: a compose file
// fronting its app with an nginx whose host port is a variable, deployed with
// that variable set to 80 — the port Traefik holds.
const portfolioStack = `services:
  portfolio:
    build:
      context: .
    expose:
      - "4321"
    networks:
      - portfolio-net

  nginx:
    image: nginx:alpine
    ports:
      - "${HTTP_PORT:-8080}:80"
    depends_on:
      portfolio:
        condition: service_healthy
    networks:
      - portfolio-net

networks:
  portfolio-net:
`

func TestAdaptComposeResolvesParameterisedHostPort(t *testing.T) {
	opt := testOptions("", 80)
	opt.Env = map[string]string{"HTTP_PORT": "80"}

	out, rep, err := adaptCompose([]byte(portfolioStack), opt)
	if err != nil {
		t.Fatalf("adaptCompose() error = %v", err)
	}
	if rep.Service != "nginx" {
		t.Errorf("routed service = %q, want nginx", rep.Service)
	}
	if rep.Port != 80 {
		t.Errorf("routed port = %d, want 80", rep.Port)
	}
	// The binding is what Traefik collides with, so it has to be gone — and
	// reported as the author wrote it, or they cannot find it in their file.
	if len(rep.Unpublished) != 1 || !strings.Contains(rep.Unpublished[0], "${HTTP_PORT:-8080}:80") {
		t.Errorf("unpublished = %v, want the nginx binding as written", rep.Unpublished)
	}
	if strings.Contains(string(out), "ports:") {
		t.Errorf("the generated file still publishes a host port:\n%s", out)
	}
	if !strings.Contains(string(out), `- "80"`) {
		t.Errorf("the container port was not kept as an expose entry:\n%s", out)
	}
}

// With the variable unset the default applies, and 8080 is not a port Traefik
// holds. The binding still goes: nginx fronts the stack, Traefik is its front
// door here, and 8080:80 left in would publish the site in plain HTTP on a raw
// port of the server, beside its domain. It is reported as dropped, as the
// author wrote it.
func TestAdaptComposeDropsTheFrontDoorsLaptopPort(t *testing.T) {
	out, rep, err := adaptCompose([]byte(portfolioStack), testOptions("", 80))
	if err != nil {
		t.Fatalf("adaptCompose() error = %v", err)
	}
	if rep.Service != "nginx" || rep.Port != 80 {
		t.Errorf("routed %s on %d, want nginx on 80", rep.Service, rep.Port)
	}
	if len(rep.Unpublished) != 1 || !strings.Contains(rep.Unpublished[0], "${HTTP_PORT:-8080}:80") {
		t.Errorf("unpublished = %v, want the nginx binding as written", rep.Unpublished)
	}
	if len(rep.Published) != 0 {
		t.Errorf("published = %v, want nothing left on the host", rep.Published)
	}
	if strings.Contains(string(out), "ports:") {
		t.Errorf("the generated file still publishes a host port:\n%s", out)
	}
	if got := HostPortsIn(portfolioStack, nil, "", 80); len(got) != 0 {
		t.Errorf("HostPortsIn = %+v, want nothing: the page would offer a port the stack no longer publishes", got)
	}
}

// What the front door publishes beside its routed port is the author's: an
// admin port, a metrics port.
func TestAdaptComposeKeepsTheFrontDoorsOtherPorts(t *testing.T) {
	stack := strings.Replace(portfolioStack, `      - "${HTTP_PORT:-8080}:80"`,
		`      - "${HTTP_PORT:-8080}:80"`+"\n"+`      - "9113:9113"`, 1)
	_, rep, err := adaptCompose([]byte(stack), testOptions("", 80))
	if err != nil {
		t.Fatalf("adaptCompose() error = %v", err)
	}
	if len(rep.Published) != 1 || !strings.Contains(rep.Published[0], "9113:9113") {
		t.Errorf("published = %v, want only the metrics port", rep.Published)
	}
}

// A service alone in its file is routed too, but it is no proxy in front of
// anything: the catalogue's Minecraft is routed on 25565 and publishes 25565,
// and that published port is how players join.
func TestAdaptComposeKeepsALoneServersPort(t *testing.T) {
	const game = "services:\n  minecraft:\n    image: itzg/minecraft-server\n    ports:\n      - \"25565:25565\"\n"
	_, rep, err := adaptCompose([]byte(game), testOptions("minecraft", 25565))
	if err != nil {
		t.Fatalf("adaptCompose() error = %v", err)
	}
	if len(rep.Unpublished) != 0 || len(rep.Published) != 1 {
		t.Errorf("unpublished %v, published %v: the game port must stay", rep.Unpublished, rep.Published)
	}
	if got := HostPortsIn(game, nil, "minecraft", 25565); len(got) != 1 || got[0].Lo != 25565 {
		t.Errorf("HostPortsIn = %+v, want 25565", got)
	}
}
