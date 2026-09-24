package config

import (
	"strings"
	"testing"
)

func find(t *testing.T, all []Setting, name string) Setting {
	t.Helper()
	for _, s := range all {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("%s is not reported", name)
	return Setting{}
}

// A secret is reported as set and nothing else: none of its characters, not
// even how many there are.
func TestSettingsHideSecrets(t *testing.T) {
	c := Config{AdminPassword: "correct horse battery staple"}
	for _, s := range c.Settings() {
		if strings.Contains(s.Value, "correct") || strings.Contains(s.Value, "28") {
			t.Errorf("%s gives the secret away: %q", s.Name, s.Value)
		}
	}
	if got := find(t, c.Settings(), "ADMIN_PASSWORD").Value; got != "•••••••• (set)" {
		t.Errorf("ADMIN_PASSWORD = %q", got)
	}
	if got := find(t, Config{}.Settings(), "ADMIN_PASSWORD").Value; got != "" {
		t.Errorf("an unset password is reported as %q", got)
	}
}

// Where a value came from: the environment, the .env file, or the default Load
// fell back on — which is also what an empty variable gets.
func TestSettingsSources(t *testing.T) {
	t.Setenv("ACME_EMAIL", "ops@example.com")
	t.Setenv("DOCKER_HOST", "")
	t.Setenv("ADMIN_USER", "admin")
	fromFile["ADMIN_USER"] = true
	t.Cleanup(func() { delete(fromFile, "ADMIN_USER") })

	all := Config{}.Settings()
	for name, want := range map[string]string{"ACME_EMAIL": "environment", "ADMIN_USER": ".env", "DOCKER_HOST": "default"} {
		if got := find(t, all, name).Source; got != want {
			t.Errorf("%s comes from %q, want %q", name, got, want)
		}
	}
}
