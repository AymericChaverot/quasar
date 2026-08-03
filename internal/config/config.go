package config

import (
	"os"
	"path/filepath"
	"strings"
)

// Config holds every runtime setting. Values come from environment variables;
// a .env file in the working directory (or $ENV_FILE) is loaded first as a
// fallback, so local dev config lives in a file instead of the shell.
type Config struct {
	Domain         string // root domain, e.g. "mon-vps.com"
	ListenAddr     string
	DBPath         string
	AppsDir        string // host path AND in-container path (mounted identically)
	TraefikDir     string // Traefik's config and ACME store, bind-mounted read-write
	TraefikNetwork string
	AdminUser      string
	AdminPassword  string // only used to bootstrap the first user, then ignored
	HostRootPath   string // where the host filesystem is mounted read-only, for disk stats
	BackupsDir     string
	CookieSecure   bool   // set COOKIE_SECURE=false for local dev over plain HTTP
	GitHubRepo     string // "owner/name", used for release checks and update images
	SocketNetwork  string // Docker network where the socket proxy lives
	KeyPath        string // at-rest encryption master key, next to the database
	// EdgeAuthURL is where Traefik reaches this dashboard to have a request to
	// a password-protected application authorised. It is an address on the
	// internal network, never one a visitor resolves.
	EdgeAuthURL string
}

func Load() Config {
	loadDotEnv(getenv("ENV_FILE", ".env"))

	dbPath := absPath(getenv("DB_PATH", "/opt/quasar/storage/database.sqlite"))
	appsDir := absPath(getenv("APPS_DIR", "/opt/quasar/apps"))
	return Config{
		Domain:         getenv("DOMAIN", "localhost"),
		ListenAddr:     getenv("LISTEN_ADDR", ":8080"),
		DBPath:         dbPath,
		AppsDir:        appsDir,
		TraefikDir:     absPath(getenv("TRAEFIK_DIR", filepath.Join(filepath.Dir(appsDir), "traefik"))),
		TraefikNetwork: getenv("TRAEFIK_NETWORK", "traefik-net"),
		AdminUser:      getenv("ADMIN_USER", ""),
		AdminPassword:  getenv("ADMIN_PASSWORD", ""),
		HostRootPath:   getenv("HOST_ROOT", "/"),
		BackupsDir:     absPath(getenv("BACKUPS_DIR", "/opt/quasar/backups")),
		CookieSecure:   getenv("COOKIE_SECURE", "true") != "false",
		GitHubRepo:     getenv("GITHUB_REPO", "AymericChaverot/quasar"),
		SocketNetwork:  getenv("SOCKET_NETWORK", "quasar-socket-net"),
		// The dashboard's container name on the Traefik network, which is
		// fixed by the system stack. Set it only when the dashboard runs
		// somewhere else — local development against a containerised Traefik.
		EdgeAuthURL: strings.TrimSuffix(getenv("EDGE_AUTH_URL", "http://quasar-dashboard:8080"), "/"),
		// Beside the database, on the same persisted volume, but deliberately
		// outside anything backup.Run archives.
		KeyPath: filepath.Join(filepath.Dir(dbPath), "master.key"),
	}
}

// loadDotEnv sets KEY=VALUE pairs from a file as environment variables.
// Real environment variables always win over file values, so the file is a
// convenience for development, not an override mechanism.
func loadDotEnv(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return // no .env file is a normal situation (e.g. inside the container)
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if _, exists := os.LookupEnv(key); !exists {
			os.Setenv(key, value)
		}
	}
}

// absPath makes relative paths (handy in a dev .env) absolute, which Docker
// bind mounts require for the apps directory.
func absPath(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
