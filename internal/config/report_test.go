package config

import (
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

// The admin account is not listed: after the first start it is not what
// anyone signs in with, and its password has no business in a log.
func TestSettingsLeaveOutTheAdminAccount(t *testing.T) {
	for _, s := range (Config{AdminUser: "admin", AdminPassword: "correct horse"}).Settings() {
		if s.Name == "ADMIN_USER" || s.Name == "ADMIN_PASSWORD" {
			t.Errorf("%s is reported", s.Name)
		}
	}
}

// Where a value came from: the environment, the .env file, or the default Load
// fell back on — which is also what an empty variable gets.
func TestSettingsSources(t *testing.T) {
	t.Setenv("ACME_EMAIL", "ops@example.com")
	t.Setenv("DOCKER_HOST", "")
	t.Setenv("APPS_DIR", "/opt/quasar/apps")
	fromFile["APPS_DIR"] = true
	t.Cleanup(func() { delete(fromFile, "APPS_DIR") })

	all := Config{}.Settings()
	for name, want := range map[string]string{"ACME_EMAIL": "environment", "APPS_DIR": ".env", "DOCKER_HOST": "default"} {
		if got := find(t, all, name).Source; got != want {
			t.Errorf("%s comes from %q, want %q", name, got, want)
		}
	}
}
