package db

import "testing"

// A branch of the same repository keeps the choices made about it; another
// repository starts from automatic, since the old choices were between what
// the old repository offered.
func TestUpdateAppSource(t *testing.T) {
	database := openTestDB(t)
	keyring := testKeyring(t)
	a := &App{ID: "a1", Name: "Portfolio", Subdomain: "me", DeployType: "git",
		GitURL: "https://github.com/me/portfolio.git", GitBranch: "main",
		GitBuild: GitBuildDockerfile, ComposeService: "web", Port: 80}
	if err := InsertApp(database, keyring, a); err != nil {
		t.Fatal(err)
	}

	if err := UpdateAppSource(database, "a1", a.GitURL, "next"); err != nil {
		t.Fatal(err)
	}
	got, err := GetApp(database, keyring, "a1")
	if err != nil {
		t.Fatal(err)
	}
	if got.GitBranch != "next" || got.GitBuild != GitBuildDockerfile || got.ComposeService != "web" {
		t.Errorf("after a branch switch: branch %q, build %q, service %q", got.GitBranch, got.GitBuild, got.ComposeService)
	}

	if err := UpdateAppSource(database, "a1", "https://github.com/me/portfolio-v2.git", "main"); err != nil {
		t.Fatal(err)
	}
	got, err = GetApp(database, keyring, "a1")
	if err != nil {
		t.Fatal(err)
	}
	if got.GitURL != "https://github.com/me/portfolio-v2.git" || got.GitBranch != "main" {
		t.Errorf("after the move: url %q, branch %q", got.GitURL, got.GitBranch)
	}
	if got.GitBuild != GitBuildAuto || got.ComposeService != "" {
		t.Errorf("after the move the old choices remain: build %q, service %q", got.GitBuild, got.ComposeService)
	}
	if got.Name != "Portfolio" || got.Subdomain != "me" {
		t.Errorf("the move changed the app itself: name %q, subdomain %q", got.Name, got.Subdomain)
	}
}
