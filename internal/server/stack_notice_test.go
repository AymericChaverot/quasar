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
	data := map[string]any{"Drift": drift, "Version": "v1.2.3"}
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
		"cd /opt/quasar &amp;&amp; git pull --ff-only &amp;&amp; docker compose up -d",
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
