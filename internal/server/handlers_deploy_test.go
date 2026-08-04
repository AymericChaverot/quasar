package server

import (
	"strings"
	"testing"

	"quasar/internal/db"
	"quasar/internal/docker"
)

// The panel opens a stream a viewer would only get a 403 from, and shows build
// output a viewer is not entitled to. Both halves have to agree.
func TestDeployPanelIsAdminOnly(t *testing.T) {
	s := testServer(t)
	app := AppView{
		App:    &db.App{ID: "abcd1234", Name: "Blog", Subdomain: "blog", DeployType: "image", ImageRef: "nginx"},
		Status: docker.AppStatus{State: "running"},
		Domain: "example.com",
	}

	var admin, viewer strings.Builder
	if err := s.pages["app_detail"].ExecuteTemplate(&admin, "layout",
		map[string]any{"Title": "Blog", "App": app, "IsAdmin": true}); err != nil {
		t.Fatal(err)
	}
	if err := s.pages["app_detail"].ExecuteTemplate(&viewer, "layout",
		map[string]any{"Title": "Blog", "App": app}); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(admin.String(), "/apps/abcd1234/deploy-log") {
		t.Error("an admin's page does not carry the deploy panel")
	}
	if strings.Contains(viewer.String(), "deploy-log") {
		t.Error("a viewer's page opens the deploy stream, which it cannot read")
	}
}

// Every line in the pane is bytes off a build's stdout, which is
// attacker-controlled for any repository a user chose to deploy.
func TestRenderDeployLineEscapesTheBuild(t *testing.T) {
	got := renderDeployLine(docker.DeployLine{Text: `RUN echo "<script>alert(1)</script>"`})
	if strings.Contains(got, "<script>") {
		t.Errorf("markup reached the pane unescaped: %s", got)
	}
	if !strings.Contains(got, "&lt;script&gt;") {
		t.Errorf("the line did not survive escaping: %s", got)
	}
}

// An SSE frame is newline-delimited, so a rendered line carrying one would
// split into two events and truncate itself.
func TestRenderDeployLineIsOneLine(t *testing.T) {
	if got := renderDeployLine(docker.DeployLine{Text: "first\nsecond"}); strings.Contains(got, "\n") {
		t.Errorf("rendered line carries a newline: %q", got)
	}
}

func TestRenderDeployLineMarksQuasarsOwnCommentary(t *testing.T) {
	note := renderDeployLine(docker.DeployLine{Note: true, Text: "pulling nginx:latest"})
	if !strings.Contains(note, `class="log-note"`) {
		t.Errorf("a note is not marked as one: %s", note)
	}
	out := renderDeployLine(docker.DeployLine{Text: "Step 1/4 : FROM nginx"})
	if strings.Contains(out, "log-note") {
		t.Errorf("build output was marked as commentary: %s", out)
	}
}
