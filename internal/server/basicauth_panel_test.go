package server

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"quasar/internal/db"
	"quasar/internal/docker"
)

// renderBasicAuth renders the password protection panel for an app in a given
// state and returns the HTML.
func renderBasicAuth(t *testing.T, v AppView) string {
	t.Helper()
	s := testServer(t)
	if v.App == nil {
		v.App = &db.App{ID: "beef0001", Name: "API", Subdomain: "api", DeployType: "image"}
	}
	v.Domain, v.IsAdmin = "example.com", true
	if v.Status.State == "" {
		v.Status.State = "running"
	}
	var out bytes.Buffer
	if err := s.pages["dashboard"].ExecuteTemplate(&out, "basic_auth_panel", v); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func protectedApp() *db.App {
	return &db.App{
		ID: "beef0001", Name: "API", Subdomain: "api", DeployType: "image",
		BasicAuthUser: "ops", BasicAuthHash: "$2y$05$hash",
	}
}

// The reported bug: saving redrew nothing, so Disable only appeared after a
// manual reload and the operator could not tell whether anything was stored.
// The panel is the answer to its own form, so it has to carry the state back.
func TestBasicAuthPanelShowsWhatWasSaved(t *testing.T) {
	off := renderBasicAuth(t, AppView{})
	if strings.Contains(off, `value="1"`) {
		t.Errorf("an unprotected app offers Disable:\n%s", off)
	}
	if !strings.Contains(off, "badge-muted") {
		t.Errorf("an unprotected app is not reported as open:\n%s", off)
	}

	on := renderBasicAuth(t, AppView{App: protectedApp()})
	if !strings.Contains(on, `value="1"`) {
		t.Errorf("a protected app does not offer Disable:\n%s", on)
	}
	if !strings.Contains(on, "ops") {
		t.Errorf("a protected app does not name its user:\n%s", on)
	}
}

// Both buttons post the same form, so the validation that makes Save reject an
// empty password would also block Disable — leaving protection impossible to
// remove without inventing a password first.
func TestBasicAuthPanelLetsDisableSkipValidation(t *testing.T) {
	got := renderBasicAuth(t, AppView{App: protectedApp()})
	disable := got[strings.Index(got, `name="disable"`):]
	disable = disable[:strings.Index(disable, ">")]
	if !strings.Contains(disable, "formnovalidate") {
		t.Errorf("Disable is subject to the form's validation:\n%s", disable)
	}
	if !strings.Contains(got, fmt.Sprintf(`minlength="%d"`, basicAuthMinLength)) {
		t.Errorf("the password field does not enforce the length the handler requires:\n%s", got)
	}
}

// Turning protection on changes the router a container is created with, so a
// save alone changes nothing a visitor meets. This is what stops the panel
// claiming an app is protected while its running container still lets everyone
// through — the exact confusion of "I saved a password and the site did not
// ask for it".
func TestBasicAuthPanelReportsAPendingRedeploy(t *testing.T) {
	tests := []struct {
		name string
		view AppView
		want string
	}{
		{
			name: "saved but not deployed",
			view: AppView{App: protectedApp(), AuthPending: true},
			want: "not in front of the app yet",
		},
		{
			name: "disabled but still deployed",
			view: AppView{AuthPending: true},
			want: "still asks visitors to sign in",
		},
		{
			name: "deployed and enforced",
			view: AppView{App: protectedApp()},
			want: "enforces it",
		},
		// Nothing is serving, so there is no container to be out of step with:
		// claiming visitors are being asked for a password would be a lie in
		// the one direction that matters.
		{
			name: "protected before the first deploy",
			view: AppView{App: protectedApp(), Status: docker.AppStatus{State: "not deployed"}},
			want: "from the first deploy",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := renderBasicAuth(t, tc.view); !strings.Contains(got, tc.want) {
				t.Errorf("panel does not say %q:\n%s", tc.want, got)
			}
		})
	}
}

// The state is polled so a redeploy is seen to have applied the password
// without a reload. That is only safe while the polled fragment holds no
// inputs: swapping a form every five seconds would wipe a password mid-typing.
func TestBasicAuthPolledStateCarriesNoForm(t *testing.T) {
	s := testServer(t)
	v := AppView{App: protectedApp(), Domain: "example.com", IsAdmin: true}
	v.Status.State = "running"
	var out bytes.Buffer
	if err := s.pages["dashboard"].ExecuteTemplate(&out, "basic_auth_state", v); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if strings.Contains(got, "<form") || strings.Contains(got, "<input") {
		t.Errorf("the polled fragment holds a form, so polling would wipe it:\n%s", got)
	}
	for _, want := range []string{`id="basic-auth-state"`, `hx-get="/partials/apps/beef0001/basic-auth"`} {
		if !strings.Contains(got, want) {
			t.Errorf("the fragment cannot refresh itself, %s is missing:\n%s", want, got)
		}
	}
}

// A compose file with its own Traefik labels is run exactly as written, so
// these credentials are stored and never applied. Reporting a redeploy would
// send the operator to do one that cannot help.
func TestBasicAuthPanelSaysNothingIsAppliedToAnAuthorRoutedStack(t *testing.T) {
	got := renderBasicAuth(t, AppView{
		App:         protectedApp(),
		AuthPending: true,
		Compose:     docker.ComposeAdaptation{Author: true, Service: "web"},
	})
	if !strings.Contains(got, "never put in front of the app") {
		t.Errorf("panel does not say the credentials are not applied:\n%s", got)
	}
	if strings.Contains(got, "redeploy owed") {
		t.Errorf("panel asks for a redeploy that would change nothing:\n%s", got)
	}
}
