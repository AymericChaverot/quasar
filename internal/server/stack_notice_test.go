package server

import (
	"bytes"
	"strings"
	"testing"
)

func renderStackNotice(t *testing.T, drift []string) string {
	t.Helper()
	s := testServer(t)
	var buf bytes.Buffer
	data := map[string]any{"Drift": drift, "Version": "v1.2.3", "Command": stackUpdateCommand}
	if err := s.pages["system"].ExecuteTemplate(&buf, "system_stack", data); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// An install behind the compose file is told what it lacks and what to run.
func TestStackNoticeSaysWhatToRun(t *testing.T) {
	html := renderStackNotice(t, []string{"quasar-dashboard does not mount /opt/quasar/traefik at /opt/quasar/traefik"})
	for _, want := range []string{
		"older than v1.2.3",
		"does not mount /opt/quasar/traefik",
		// As root: setup.sh installs as root, and /opt/quasar belongs to it.
		"sudo sh -c &#39;cd /opt/quasar &amp;&amp; git pull --ff-only &amp;&amp; docker compose up -d&#39;",
		// A hand-edited compose file stops the pull; the notice says what then.
		"sudo git -C /opt/quasar diff",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("the notice does not say %q", want)
		}
	}
}

// An install that is up to date gets nothing, not an empty box.
func TestStackNoticeAbsentWhenUpToDate(t *testing.T) {
	if html := strings.TrimSpace(renderStackNotice(t, nil)); html != "" {
		t.Errorf("an up-to-date install renders %q", html)
	}
}

// Once Traefik fills the email in itself, an install whose traefik.yml still
// carries it is told how to restore the file.
func TestStackNoticeOffersToRestoreTraefikConfig(t *testing.T) {
	s := testServer(t)
	var buf bytes.Buffer
	data := map[string]any{"TraefikRestore": true, "Version": "v1.2.3", "Command": stackUpdateCommand}
	if err := s.pages["system"].ExecuteTemplate(&buf, "system_stack", data); err != nil {
		t.Fatal(err)
	}
	html := buf.String()
	if !strings.Contains(html, "sudo git -C /opt/quasar checkout -- traefik/traefik.yml") {
		t.Error("the notice does not say how to restore traefik.yml")
	}
	if strings.Contains(html, "older than") {
		t.Error("an install with no drift is told its stack is old")
	}
}
