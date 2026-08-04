package server

import (
	"bytes"
	"strings"
	"testing"

	"quasar/internal/db"
	"quasar/internal/docker"
)

// renderLimits renders the resource limits panel for an app in a given state
// and returns the HTML.
func renderLimits(t *testing.T, v AppView) string {
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
	if err := s.pages["dashboard"].ExecuteTemplate(&out, "limits_panel", v); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func limitedApp(cpu float64, memMB int64) *db.App {
	return &db.App{
		ID: "beef0001", Name: "API", Subdomain: "api", DeployType: "image",
		CPULimit: cpu, MemLimitMB: memMB,
	}
}

// The form has to come back carrying what was stored, since it is also what the
// operator reads to know the save landed.
func TestLimitsPanelShowsWhatWasSaved(t *testing.T) {
	got := renderLimits(t, AppView{
		App:    limitedApp(1.5, 512),
		Limits: docker.LimitsState{Known: true, CPULimit: 1.5, MemLimitMB: 512},
	})
	if !strings.Contains(got, `value="1.5"`) || !strings.Contains(got, `value="512"`) {
		t.Errorf("the form does not carry the stored limits back:\n%s", got)
	}
	if !strings.Contains(got, "1.5 CPU · 512 MB") {
		t.Errorf("the panel does not name the stored limits:\n%s", got)
	}

	// An unlimited app must leave both fields empty rather than showing a 0 the
	// placeholder already explains.
	open := renderLimits(t, AppView{Limits: docker.LimitsState{Known: true}})
	if strings.Contains(open, `value="0"`) {
		t.Errorf("an unlimited app prefills its fields with 0:\n%s", open)
	}
	if !strings.Contains(open, "unlimited") {
		t.Errorf("an unlimited app is not reported as unlimited:\n%s", open)
	}
}

// The whole point of the panel: a tightened limit is live, a lifted one is not,
// and the operator has to be told which of the two just happened.
func TestLimitsPanelReportsWhatTheContainerEnforces(t *testing.T) {
	tests := []struct {
		name string
		view AppView
		want string
	}{
		{
			name: "applied to the running container",
			view: AppView{
				App:    limitedApp(1, 256),
				Limits: docker.LimitsState{Known: true, CPULimit: 1, MemLimitMB: 256},
			},
			want: "running container enforces this",
		},
		{
			// Removing a limit is the one change Docker cannot make in place.
			name: "limit lifted, container still holding it",
			view: AppView{
				App:    &db.App{ID: "beef0001", Name: "API", Subdomain: "api", DeployType: "image"},
				Limits: docker.LimitsState{Known: true, CPULimit: 1, MemLimitMB: 256, Pending: true},
			},
			want: "still running under 1 CPU · 256 MB",
		},
		{
			name: "nothing deployed yet",
			view: AppView{App: limitedApp(1, 256), Status: docker.AppStatus{State: "not deployed"}},
			want: "from the next deploy",
		},
		{
			name: "the daemon refused the change",
			view: AppView{
				App:         limitedApp(64, 256),
				Limits:      docker.LimitsState{Known: true, CPULimit: 1, MemLimitMB: 256, Pending: true},
				LimitsError: "the running container refused the change: Range of CPUs is from 0.01 to 4.00",
			},
			want: "Range of CPUs",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := renderLimits(t, tc.view); !strings.Contains(got, tc.want) {
				t.Errorf("panel does not say %q:\n%s", tc.want, got)
			}
		})
	}
}

// A stack's containers are limited by its compose file; Quasar writes no
// resource limits into it. Asking for a redeploy would send the operator to do
// one that cannot help.
func TestLimitsPanelSaysNothingIsAppliedToAStack(t *testing.T) {
	got := renderLimits(t, AppView{App: limitedApp(1, 256), Stack: true})
	if !strings.Contains(got, "runs as a compose stack") {
		t.Errorf("panel does not say the limits are not applied:\n%s", got)
	}
	if strings.Contains(got, "redeploy owed") {
		t.Errorf("panel asks for a redeploy that would change nothing:\n%s", got)
	}
}

// The state is polled so a redeploy is seen to have lifted a limit without a
// reload. That is only safe while the polled fragment holds no inputs: swapping
// a form every five seconds would wipe a figure mid-typing.
func TestLimitsPolledStateCarriesNoForm(t *testing.T) {
	s := testServer(t)
	v := AppView{App: limitedApp(1, 256), Domain: "example.com", IsAdmin: true}
	v.Status.State = "running"
	var out bytes.Buffer
	if err := s.pages["dashboard"].ExecuteTemplate(&out, "limits_state", v); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if strings.Contains(got, "<form") || strings.Contains(got, "<input") {
		t.Errorf("the polled fragment holds a form, so polling would wipe it:\n%s", got)
	}
	for _, want := range []string{`id="limits-state"`, `hx-get="/partials/apps/beef0001/limits"`} {
		if !strings.Contains(got, want) {
			t.Errorf("the fragment cannot refresh itself, %s is missing:\n%s", want, got)
		}
	}
}
