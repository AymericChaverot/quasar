package config

import (
	"os"
)

// fromFile names the variables loadDotEnv set, so Settings can say a value
// came from the file rather than from the process's own environment.
var fromFile = map[string]bool{}

// Setting is one variable the dashboard read as it started: its value as it is
// actually used — a path made absolute, a default filled in — and where it
// came from.
type Setting struct {
	Name  string
	Value string
	// Source is "environment", ".env" or "default".
	Source string
}

// Settings is what the start-up sequence lists under its config line: the
// settings worth checking after a restart that no other line of it already
// shows — the domain, the database, the address and the mode each have one —
// where the data goes, how Docker is reached, and the accounts. A secret's
// value never leaves here: it says only whether one is set.
func (c Config) Settings() []Setting {
	return []Setting{
		setting("APPS_DIR", c.AppsDir),
		setting("BACKUPS_DIR", c.BackupsDir),
		setting("DOCKER_HOST", os.Getenv("DOCKER_HOST")),
		setting("ACME_EMAIL", os.Getenv("ACME_EMAIL")),
		setting("ADMIN_USER", c.AdminUser),
		secret("ADMIN_PASSWORD", c.AdminPassword),
	}
}

func setting(name, value string) Setting {
	return Setting{Name: name, Value: value, Source: source(name)}
}

// secret reports a secret by whether it is set, and nothing more: not a
// prefix, not its length. Logs get pasted into tickets and chats, and even a
// few characters of a password make it that much easier to guess.
func secret(name, value string) Setting {
	shown := ""
	if value != "" {
		shown = "•••••••• (set)"
	}
	return Setting{Name: name, Value: shown, Source: source(name)}
}

func source(name string) string {
	// Empty counts as unset, as it does in Load.
	switch {
	case os.Getenv(name) == "":
		return "default"
	case fromFile[name]:
		return ".env"
	default:
		return "environment"
	}
}
