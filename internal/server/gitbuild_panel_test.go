package server

import (
	"bytes"
	"strings"
	"testing"

	"quasar/internal/db"
	"quasar/internal/docker"
)

// renderGitBuild renders the build panel for a checkout offering the given
// files and returns the HTML.
func renderGitBuild(t *testing.T, build docker.GitBuild) string {
	t.Helper()
	s := testServer(t)
	v := AppView{
		App:     &db.App{ID: "beef0001", Name: "API", Subdomain: "api", DeployType: "git", GitBuild: build.Choice},
		Domain:  "example.com",
		Build:   build,
		IsAdmin: true,
	}
	var out bytes.Buffer
	if err := s.pages["dashboard"].ExecuteTemplate(&out, "git_build_panel", v); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

// The reported bug: a repository with only a compose file still offered
// "Dockerfile", which no deploy could ever honour.
func TestGitBuildPanelHidesImpossibleChoices(t *testing.T) {
	tests := []struct {
		name       string
		build      docker.GitBuild
		wantOption string // the option value that must NOT be offered
	}{
		{
			name:       "compose only",
			build:      docker.GitBuild{Mode: "compose", HasCompose: true, ComposeFile: "docker-compose.yml"},
			wantOption: `value="dockerfile"`,
		},
		{
			name:       "dockerfile only",
			build:      docker.GitBuild{Mode: "dockerfile", HasDockerfile: true},
			wantOption: `value="compose"`,
		},
		{
			name:       "nothing checked out yet",
			build:      docker.GitBuild{},
			wantOption: `value="dockerfile"`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := renderGitBuild(t, tc.build)
			if strings.Contains(got, tc.wantOption) {
				t.Errorf("panel offers %s, which this checkout cannot honour:\n%s", tc.wantOption, got)
			}
			if strings.Contains(got, "<select") {
				t.Errorf("panel shows a choice where there is only one way to build:\n%s", got)
			}
		})
	}
}

// A repository holding both is the only case where the choice means something,
// and then both must be on offer.
func TestGitBuildPanelOffersBothWhenBothExist(t *testing.T) {
	got := renderGitBuild(t, docker.GitBuild{
		Mode: "compose", HasCompose: true, HasDockerfile: true, ComposeFile: "docker-compose.yml",
	})
	for _, want := range []string{"<select", `value="dockerfile"`, `value="compose"`, `value=""`} {
		if !strings.Contains(got, want) {
			t.Errorf("panel does not offer %s:\n%s", want, got)
		}
	}
}

// Hiding an option in the form is not enforcement — a hand-made POST bypasses
// it. This is what actually keeps an impossible mode out of the database.
func TestGitBuildChoiceRejectsWhatTheCheckoutLacks(t *testing.T) {
	composeOnly := docker.GitBuild{Mode: "compose", HasCompose: true}
	dockerfileOnly := docker.GitBuild{Mode: "dockerfile", HasDockerfile: true}
	both := docker.GitBuild{Mode: "compose", HasCompose: true, HasDockerfile: true}

	tests := []struct {
		name    string
		mode    string
		build   docker.GitBuild
		wantErr bool
	}{
		{"dockerfile asked of a compose-only repo", db.GitBuildDockerfile, composeOnly, true},
		{"compose asked of a dockerfile-only repo", db.GitBuildCompose, dockerfileOnly, true},
		{"dockerfile when there is one", db.GitBuildDockerfile, both, false},
		{"compose when there is one", db.GitBuildCompose, both, false},
		{"automatic is always allowed", db.GitBuildAuto, composeOnly, false},
		// Nothing cloned yet, so nothing is known to be impossible.
		{"no checkout yet", db.GitBuildDockerfile, docker.GitBuild{}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := gitBuildChoiceError(tc.mode, tc.build)
			if (got != "") != tc.wantErr {
				t.Errorf("gitBuildChoiceError(%q) = %q, wantErr %v", tc.mode, got, tc.wantErr)
			}
		})
	}
}

// A stale choice — set when the repository had both, kept after it lost one —
// must stay correctable, so the form comes back even though there is now only
// one buildable option.
func TestGitBuildPanelLetsAStaleChoiceBeCorrected(t *testing.T) {
	got := renderGitBuild(t, docker.GitBuild{
		Mode: "compose", Choice: db.GitBuildDockerfile, HasCompose: true, ComposeFile: "docker-compose.yml",
	})
	if !strings.Contains(got, "<select") {
		t.Errorf("a choice the checkout cannot honour must remain editable:\n%s", got)
	}
	if strings.Contains(got, `value="dockerfile"`) {
		t.Errorf("the impossible option is still on offer:\n%s", got)
	}
}
