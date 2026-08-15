// Package catalog holds the one-click application templates. Selecting one
// prefills the "new application" form; {{RANDOM}} placeholders in env vars are
// replaced with a fresh secret at that moment.
//
// An entry is either a single image or a whole compose stack. Most of the
// self-hosted software people actually want is several containers — an app, a
// database and a cache — so the single-image shape the catalogue started with
// could only ever have described the minority of it.
//
// Compose entries interpolate ${VAR} from the app's .env, which Quasar writes
// beside the compose file and passes with --env-file, so a stack's secrets are
// generated once here and referenced from both places. Relative paths resolve
// against apps/<id>/, and apps/<id>/data is created before the first deploy —
// stacks bind their state under ./data so the backup archive picks it up,
// which a named volume would not.
package catalog

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
)

// Categories in the order the catalogue presents them: the reasons people
// install a server at all, roughly most common first.
var Categories = []string{
	"Media",
	"Files & sync",
	"Downloads",
	"Notes & docs",
	"Tasks & projects",
	"Dashboards & monitoring",
	"Security",
	"Development",
	"Automation",
	"Reading & RSS",
	"Analytics",
	"Websites",
	"Utilities",
	"Databases",
	"Game servers",
}

type Template struct {
	ID          string
	Name        string
	Description string
	Category    string

	// DeployType is "image" or "compose"; empty reads as "image".
	DeployType string

	// Image deploys.
	ImageRef  string
	DataMount string // container path bound to apps/<id>/data, empty = no volume

	// Compose deploys. ComposeService names the service the domain routes to,
	// left empty where the file has only one plausible candidate and Quasar
	// can work it out itself.
	Compose        string
	ComposeService string

	// Port the domain routes to, for either deploy type.
	Port int

	// Env is .env content. {{RANDOM}} becomes a fresh secret per selection.
	Env string

	// Raw marks a server that speaks its own protocol rather than HTTP, so it
	// is reached at the host's address and port instead of at the subdomain.
	// The smoke test reads this too: an HTTP probe would never pass on one.
	Raw bool

	// NeedsSetup names what the operator has to supply before the app will
	// start at all — credentials for something outside this server, which
	// Quasar cannot invent. These are the only entries that do not come up on
	// their own, so they say so on the card rather than failing silently after
	// the deploy, and the smoke test skips them for the same reason.
	NeedsSetup string

	// Note is the one thing worth reading before the form is submitted: a URL
	// that has to be filled in, or which port a Raw server listens on.
	Note string
}

// Caveat is the note as the page shows it: what the entry needs before it will
// run, then the standing explanation for a server that does not speak HTTP,
// then whatever else the entry has to say.
func (t Template) Caveat() string {
	var parts []string
	if t.NeedsSetup != "" {
		parts = append(parts, "Does not start on its own: "+t.NeedsSetup)
	}
	if t.Raw {
		parts = append(parts, noteRawPort)
	}
	if t.Note != "" {
		parts = append(parts, t.Note)
	}
	return strings.Join(parts, " ")
}

// Type is the deploy type to prefill, defaulting to a single image.
func (t Template) Type() string {
	if t.DeployType == "" {
		return "image"
	}
	return t.DeployType
}

// noteRawPort is carried by every server that speaks its own protocol rather
// than HTTP. Traefik holds :80 and :443 and routes by Host header, which a
// game client never sends, so these are reached at the host's own address and
// the subdomain the form insists on stays unused.
const noteRawPort = "Not an HTTP app: reach it at your server's IP on the port below, not at the subdomain. " +
	"The subdomain is still required by the form but will not serve anything."

var Templates = []Template{
	// --- Media -------------------------------------------------------------
	{
		ID: "jellyfin", Name: "Jellyfin", Description: "Movies, TV and music streaming",
		Category: "Media", ImageRef: "jellyfin/jellyfin:latest", Port: 8096, DataMount: "/config",
	},
	{
		ID: "immich", Name: "Immich", Description: "Photo and video library for phones",
		Category: "Media", DeployType: "compose", Port: 2283, ComposeService: "immich-server",
		Env: "DB_PASSWORD={{RANDOM}}",
		Compose: `services:
  immich-server:
    image: ghcr.io/immich-app/immich-server:release
    volumes:
      - ./data/upload:/data
    environment:
      DB_HOSTNAME: database
      DB_USERNAME: postgres
      DB_PASSWORD: ${DB_PASSWORD}
      DB_DATABASE_NAME: immich
      REDIS_HOSTNAME: redis
    depends_on:
      redis:
        condition: service_healthy
      database:
        condition: service_healthy
    restart: unless-stopped

  immich-machine-learning:
    image: ghcr.io/immich-app/immich-machine-learning:release
    volumes:
      - ./data/model-cache:/cache
    restart: unless-stopped

  redis:
    image: redis:7-alpine
    healthcheck:
      test: ["CMD", "redis-cli", "ping"]
      interval: 5s
      timeout: 5s
      retries: 30
    restart: unless-stopped

  database:
    image: ghcr.io/immich-app/postgres:14-vectorchord0.4.3-pgvectors0.2.0
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U $$POSTGRES_USER -d $$POSTGRES_DB"]
      interval: 5s
      timeout: 5s
      retries: 30
    environment:
      POSTGRES_USER: postgres
      POSTGRES_PASSWORD: ${DB_PASSWORD}
      POSTGRES_DB: immich
    volumes:
      - ./data/db:/var/lib/postgresql/data
    restart: unless-stopped
`,
	},
	{
		ID: "navidrome", Name: "Navidrome", Description: "Music streaming server",
		Category: "Media", ImageRef: "deluan/navidrome:latest", Port: 4533, DataMount: "/data",
	},
	{
		ID: "audiobookshelf", Name: "Audiobookshelf", Description: "Audiobooks and podcasts",
		Category: "Media", ImageRef: "ghcr.io/advplyr/audiobookshelf:latest", Port: 80, DataMount: "/config",
	},
	{
		ID: "calibre-web", Name: "Calibre-Web", Description: "Browse and read an ebook library",
		Category: "Media", ImageRef: "lscr.io/linuxserver/calibre-web:latest", Port: 8083, DataMount: "/config",
		Env: "PUID=1000\nPGID=1000\nTZ=Etc/UTC",
	},

	// --- Files & sync ------------------------------------------------------
	{
		ID: "nextcloud", Name: "Nextcloud", Description: "Files, calendar and contacts",
		Category: "Files & sync", ImageRef: "nextcloud:apache", Port: 80, DataMount: "/var/www/html",
		Note: "Ships with SQLite, which is fine for a handful of users. Add Postgres for a bigger install.",
	},
	{
		ID: "syncthing", Name: "Syncthing", Description: "Continuous file sync between devices",
		Category: "Files & sync", ImageRef: "syncthing/syncthing:latest", Port: 8384, DataMount: "/var/syncthing",
		Note: "The web UI is routed. Device-to-device sync needs TCP/UDP 22000, which Traefik does not carry.",
	},
	{
		ID: "filebrowser", Name: "File Browser", Description: "Web file manager",
		Category: "Files & sync", ImageRef: "filebrowser/filebrowser:latest", Port: 80, DataMount: "/srv",
	},

	// --- Downloads ---------------------------------------------------------
	{
		ID: "qbittorrent", Name: "qBittorrent", Description: "BitTorrent client with a web UI",
		Category: "Downloads", ImageRef: "lscr.io/linuxserver/qbittorrent:latest", Port: 8080, DataMount: "/config",
		Env: "PUID=1000\nPGID=1000\nTZ=Etc/UTC\nWEBUI_PORT=8080",
	},
	{
		ID: "sonarr", Name: "Sonarr", Description: "TV series library manager",
		Category: "Downloads", ImageRef: "lscr.io/linuxserver/sonarr:latest", Port: 8989, DataMount: "/config",
		Env: "PUID=1000\nPGID=1000\nTZ=Etc/UTC",
	},
	{
		ID: "radarr", Name: "Radarr", Description: "Film library manager",
		Category: "Downloads", ImageRef: "lscr.io/linuxserver/radarr:latest", Port: 7878, DataMount: "/config",
		Env: "PUID=1000\nPGID=1000\nTZ=Etc/UTC",
	},
	{
		ID: "prowlarr", Name: "Prowlarr", Description: "Indexer manager for the *arr apps",
		Category: "Downloads", ImageRef: "lscr.io/linuxserver/prowlarr:latest", Port: 9696, DataMount: "/config",
		Env: "PUID=1000\nPGID=1000\nTZ=Etc/UTC",
	},
	{
		ID: "sabnzbd", Name: "SABnzbd", Description: "Usenet downloader",
		Category: "Downloads", ImageRef: "lscr.io/linuxserver/sabnzbd:latest", Port: 8080, DataMount: "/config",
		Env: "PUID=1000\nPGID=1000\nTZ=Etc/UTC",
	},
	{
		ID: "jellyseerr", Name: "Jellyseerr", Description: "Media requests for Jellyfin and Plex",
		Category: "Downloads", ImageRef: "fallenbagel/jellyseerr:latest", Port: 5055, DataMount: "/app/config",
	},

	// --- Notes & docs ------------------------------------------------------
	{
		ID: "memos", Name: "Memos", Description: "Lightweight notes, one thought per card",
		Category: "Notes & docs", ImageRef: "neosmemo/memos:stable", Port: 5230, DataMount: "/var/opt/memos",
	},
	{
		ID: "trilium", Name: "Trilium Notes", Description: "Hierarchical personal knowledge base",
		Category: "Notes & docs", ImageRef: "triliumnext/notes:latest", Port: 8080, DataMount: "/home/node/trilium-data",
	},
	{
		ID: "outline", Name: "Outline", Description: "Team wiki and knowledge base",
		Category: "Notes & docs", DeployType: "compose", Port: 3000, ComposeService: "outline",
		Env: "# Outline refuses to start until URL matches the address you serve it on.\n" +
			"URL={{URL}}\nSECRET_KEY={{RANDOM}}{{RANDOM}}\nUTILS_SECRET={{RANDOM}}{{RANDOM}}\n" +
			"DB_PASSWORD={{RANDOM}}",
		Note: "The public address in the env below was filled in from the subdomain; change it there too if you change the subdomain.",
		Compose: `services:
  outline:
    image: outlinewiki/outline:latest
    environment:
      URL: ${URL}
      SECRET_KEY: ${SECRET_KEY}
      UTILS_SECRET: ${UTILS_SECRET}
      DATABASE_URL: postgres://outline:${DB_PASSWORD}@postgres:5432/outline
      REDIS_URL: redis://redis:6379
      FILE_STORAGE: local
      FILE_STORAGE_LOCAL_ROOT_DIR: /var/lib/outline/data
      PGSSLMODE: disable
    volumes:
      - ./data/storage:/var/lib/outline/data
    depends_on:
      postgres:
        condition: service_healthy
      redis:
        condition: service_healthy
    restart: unless-stopped

  postgres:
    image: postgres:16-alpine
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U $$POSTGRES_USER -d $$POSTGRES_DB"]
      interval: 5s
      timeout: 5s
      retries: 30
    environment:
      POSTGRES_USER: outline
      POSTGRES_PASSWORD: ${DB_PASSWORD}
      POSTGRES_DB: outline
    volumes:
      - ./data/db:/var/lib/postgresql/data
    restart: unless-stopped

  redis:
    image: redis:7-alpine
    healthcheck:
      test: ["CMD", "redis-cli", "ping"]
      interval: 5s
      timeout: 5s
      retries: 30
    restart: unless-stopped
`,
	},
	{
		ID: "docmost", Name: "Docmost", Description: "Collaborative documentation workspace",
		Category: "Notes & docs", DeployType: "compose", Port: 3000, ComposeService: "docmost",
		Env:  "APP_URL={{URL}}\nAPP_SECRET={{RANDOM}}{{RANDOM}}\nDB_PASSWORD={{RANDOM}}",
		Note: "The public address in the env below was filled in from the subdomain; change it there too if you change the subdomain.",
		Compose: `services:
  docmost:
    image: docmost/docmost:latest
    environment:
      APP_URL: ${APP_URL}
      APP_SECRET: ${APP_SECRET}
      DATABASE_URL: postgresql://docmost:${DB_PASSWORD}@db:5432/docmost?schema=public
      REDIS_URL: redis://redis:6379
    volumes:
      - ./data/storage:/app/data/storage
    depends_on:
      db:
        condition: service_healthy
      redis:
        condition: service_healthy
    restart: unless-stopped

  db:
    image: postgres:16-alpine
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U $$POSTGRES_USER -d $$POSTGRES_DB"]
      interval: 5s
      timeout: 5s
      retries: 30
    environment:
      POSTGRES_USER: docmost
      POSTGRES_PASSWORD: ${DB_PASSWORD}
      POSTGRES_DB: docmost
    volumes:
      - ./data/db:/var/lib/postgresql/data
    restart: unless-stopped

  redis:
    image: redis:7-alpine
    healthcheck:
      test: ["CMD", "redis-cli", "ping"]
      interval: 5s
      timeout: 5s
      retries: 30
    restart: unless-stopped
`,
	},
	{
		ID: "wikijs", Name: "Wiki.js", Description: "Wiki with a rich editor",
		Category: "Notes & docs", DeployType: "compose", Port: 3000, ComposeService: "wiki",
		Env: "DB_PASSWORD={{RANDOM}}",
		Compose: `services:
  wiki:
    image: ghcr.io/requarks/wiki:2
    environment:
      DB_TYPE: postgres
      DB_HOST: db
      DB_PORT: 5432
      DB_USER: wiki
      DB_PASS: ${DB_PASSWORD}
      DB_NAME: wiki
    depends_on:
      db:
        condition: service_healthy
    restart: unless-stopped

  db:
    image: postgres:16-alpine
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U $$POSTGRES_USER -d $$POSTGRES_DB"]
      interval: 5s
      timeout: 5s
      retries: 30
    environment:
      POSTGRES_USER: wiki
      POSTGRES_PASSWORD: ${DB_PASSWORD}
      POSTGRES_DB: wiki
    volumes:
      - ./data/db:/var/lib/postgresql/data
    restart: unless-stopped
`,
	},

	// --- Tasks & projects --------------------------------------------------
	{
		ID: "vikunja", Name: "Vikunja", Description: "To-do lists and project planning",
		Category: "Tasks & projects", ImageRef: "vikunja/vikunja:latest", Port: 3456, DataMount: "/data",
		// Vikunja keeps its database and its uploads in two different places by
		// default — /db and /app/vikunja/files — and Quasar binds one directory
		// per app. Both are pointed inside it, or the container exits on the
		// first migration because /db does not exist.
		Env: "VIKUNJA_SERVICE_PUBLICURL={{URL}}\nVIKUNJA_SERVICE_SECRET={{RANDOM}}{{RANDOM}}\n" +
			"VIKUNJA_DATABASE_PATH=/data/vikunja.db\nVIKUNJA_FILES_BASEPATH=/data/files",
		Note: "The public address in the env below was filled in from the subdomain; change it there too if you change the subdomain.",
	},
	{
		ID: "planka", Name: "Planka", Description: "Kanban boards",
		Category: "Tasks & projects", DeployType: "compose", Port: 1337, ComposeService: "planka",
		Env: "BASE_URL={{URL}}\nSECRET_KEY={{RANDOM}}{{RANDOM}}\nDB_PASSWORD={{RANDOM}}\n" +
			"ADMIN_EMAIL=admin@example.com\nADMIN_PASSWORD={{RANDOM}}",
		Note: "The first admin is created from ADMIN_EMAIL and ADMIN_PASSWORD below. The public address in the env below was filled in from the subdomain; change it there too if you change the subdomain.",
		Compose: `services:
  planka:
    image: ghcr.io/plankanban/planka:latest
    environment:
      BASE_URL: ${BASE_URL}
      SECRET_KEY: ${SECRET_KEY}
      DATABASE_URL: postgresql://planka:${DB_PASSWORD}@db/planka
      DEFAULT_ADMIN_EMAIL: ${ADMIN_EMAIL}
      DEFAULT_ADMIN_PASSWORD: ${ADMIN_PASSWORD}
      DEFAULT_ADMIN_NAME: Admin
      DEFAULT_ADMIN_USERNAME: admin
    volumes:
      - ./data/favicons:/app/public/favicons
      - ./data/user-avatars:/app/public/user-avatars
      - ./data/attachments:/app/private/attachments
    depends_on:
      db:
        condition: service_healthy
    restart: unless-stopped

  db:
    image: postgres:16-alpine
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U $$POSTGRES_USER -d $$POSTGRES_DB"]
      interval: 5s
      timeout: 5s
      retries: 30
    environment:
      POSTGRES_USER: planka
      POSTGRES_PASSWORD: ${DB_PASSWORD}
      POSTGRES_DB: planka
    volumes:
      - ./data/db:/var/lib/postgresql/data
    restart: unless-stopped
`,
	},

	// --- Dashboards & monitoring -------------------------------------------
	{
		ID: "uptime-kuma", Name: "Uptime Kuma", Description: "Uptime monitoring and alerts",
		Category: "Dashboards & monitoring", ImageRef: "louislam/uptime-kuma:1", Port: 3001, DataMount: "/app/data",
	},
	{
		ID: "homepage", Name: "Homepage", Description: "Start page for your services",
		Category: "Dashboards & monitoring", ImageRef: "ghcr.io/gethomepage/homepage:latest", Port: 3000, DataMount: "/app/config",
		Env:  "HOMEPAGE_ALLOWED_HOSTS={{HOST}}",
		Note: "Homepage answers 400 to any host it was not told about; HOMEPAGE_ALLOWED_HOSTS below is set from the subdomain, so update it if you change that.",
	},
	{
		ID: "dashy", Name: "Dashy", Description: "Configurable service dashboard",
		Category: "Dashboards & monitoring", ImageRef: "lissy93/dashy:latest", Port: 8080, DataMount: "/app/user-data",
	},
	{
		ID: "grafana", Name: "Grafana", Description: "Metrics dashboards",
		Category: "Dashboards & monitoring", ImageRef: "grafana/grafana:latest", Port: 3000, DataMount: "/var/lib/grafana",
		Env: "GF_SECURITY_ADMIN_PASSWORD={{RANDOM}}",
	},
	{
		ID: "beszel", Name: "Beszel", Description: "Lightweight server monitoring",
		Category: "Dashboards & monitoring", ImageRef: "henrygd/beszel:latest", Port: 8090, DataMount: "/beszel_data",
	},

	// --- Security ----------------------------------------------------------
	{
		ID: "vaultwarden", Name: "Vaultwarden", Description: "Bitwarden-compatible password manager",
		Category: "Security", ImageRef: "vaultwarden/server:latest", Port: 80, DataMount: "/data",
		Env: "ADMIN_TOKEN={{RANDOM}}{{RANDOM}}\nSIGNUPS_ALLOWED=false",
	},
	{
		ID: "authentik", Name: "Authentik", Description: "Single sign-on and identity provider",
		Category: "Security", DeployType: "compose", Port: 9000, ComposeService: "server",
		Env: "PG_PASS={{RANDOM}}\nAUTHENTIK_SECRET_KEY={{RANDOM}}{{RANDOM}}",
		Compose: `services:
  server:
    image: ghcr.io/goauthentik/server:latest
    command: server
    environment:
      AUTHENTIK_REDIS__HOST: redis
      AUTHENTIK_POSTGRESQL__HOST: postgresql
      AUTHENTIK_POSTGRESQL__USER: authentik
      AUTHENTIK_POSTGRESQL__NAME: authentik
      AUTHENTIK_POSTGRESQL__PASSWORD: ${PG_PASS}
      AUTHENTIK_SECRET_KEY: ${AUTHENTIK_SECRET_KEY}
    volumes:
      - ./data/media:/media
      - ./data/templates:/templates
    depends_on:
      postgresql:
        condition: service_healthy
      redis:
        condition: service_healthy
    restart: unless-stopped

  worker:
    image: ghcr.io/goauthentik/server:latest
    command: worker
    environment:
      AUTHENTIK_REDIS__HOST: redis
      AUTHENTIK_POSTGRESQL__HOST: postgresql
      AUTHENTIK_POSTGRESQL__USER: authentik
      AUTHENTIK_POSTGRESQL__NAME: authentik
      AUTHENTIK_POSTGRESQL__PASSWORD: ${PG_PASS}
      AUTHENTIK_SECRET_KEY: ${AUTHENTIK_SECRET_KEY}
    volumes:
      - ./data/media:/media
      - ./data/templates:/templates
    depends_on:
      postgresql:
        condition: service_healthy
      redis:
        condition: service_healthy
    restart: unless-stopped

  postgresql:
    image: postgres:16-alpine
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U $$POSTGRES_USER -d $$POSTGRES_DB"]
      interval: 5s
      timeout: 5s
      retries: 30
    environment:
      POSTGRES_USER: authentik
      POSTGRES_PASSWORD: ${PG_PASS}
      POSTGRES_DB: authentik
    volumes:
      - ./data/db:/var/lib/postgresql/data
    restart: unless-stopped

  redis:
    image: redis:7-alpine
    healthcheck:
      test: ["CMD", "redis-cli", "ping"]
      interval: 5s
      timeout: 5s
      retries: 30
    restart: unless-stopped
`,
	},
	{
		ID: "authelia", Name: "Authelia", Description: "Authentication gateway with 2FA",
		Category: "Security", ImageRef: "authelia/authelia:latest", Port: 9091, DataMount: "/config",
		NeedsSetup: "Authelia writes a default configuration.yml into its data directory on first run and then exits, " +
			"because the identity provider, users and access rules are yours to define. Edit that file, then redeploy. " +
			"For single sign-on that comes up on its own, Authentik is in this catalogue too.",
	},

	// --- Development -------------------------------------------------------
	{
		ID: "gitea", Name: "Gitea", Description: "Git hosting with issues and CI",
		Category: "Development", ImageRef: "gitea/gitea:latest", Port: 3000, DataMount: "/data",
		Env:  "USER_UID=1000\nUSER_GID=1000\nGITEA__server__ROOT_URL={{URL}}/",
		Note: "SSH cloning needs port 22, which Traefik does not carry. HTTPS cloning works over the subdomain.",
	},
	{
		ID: "forgejo", Name: "Forgejo", Description: "Community fork of Gitea",
		Category: "Development", ImageRef: "codeberg.org/forgejo/forgejo:11", Port: 3000, DataMount: "/data",
		Env: "USER_UID=1000\nUSER_GID=1000",
	},
	{
		ID: "registry", Name: "Docker Registry", Description: "Private container image registry",
		Category: "Development", ImageRef: "registry:2", Port: 5000, DataMount: "/var/lib/registry",
		Note: "Put basic auth on this app before pushing to it — an open registry is an open disk.",
	},
	{
		ID: "woodpecker", Name: "Woodpecker CI", Description: "Container-native CI server",
		Category: "Development", DeployType: "compose", Port: 8000, ComposeService: "woodpecker-server",
		Env: "WOODPECKER_HOST={{URL}}\nWOODPECKER_AGENT_SECRET={{RANDOM}}{{RANDOM}}\n" +
			"# Fill in from an OAuth app on your forge:\nWOODPECKER_GITEA_URL=https://CHANGE-ME\n" +
			"WOODPECKER_GITEA_CLIENT=\nWOODPECKER_GITEA_SECRET=",
		NeedsSetup: "Woodpecker authenticates against your Git forge and restart-loops until it can. " +
			"Create an OAuth application on your Gitea or Forgejo instance and fill the WOODPECKER_GITEA_* values below before deploying.",
		Compose: `services:
  woodpecker-server:
    image: woodpeckerci/woodpecker-server:latest
    environment:
      WOODPECKER_OPEN: "true"
      WOODPECKER_HOST: ${WOODPECKER_HOST}
      WOODPECKER_GITEA: "true"
      WOODPECKER_GITEA_URL: ${WOODPECKER_GITEA_URL}
      WOODPECKER_GITEA_CLIENT: ${WOODPECKER_GITEA_CLIENT}
      WOODPECKER_GITEA_SECRET: ${WOODPECKER_GITEA_SECRET}
      WOODPECKER_AGENT_SECRET: ${WOODPECKER_AGENT_SECRET}
    volumes:
      - ./data/server:/var/lib/woodpecker
    restart: unless-stopped

  woodpecker-agent:
    image: woodpeckerci/woodpecker-agent:latest
    command: agent
    environment:
      WOODPECKER_SERVER: woodpecker-server:9000
      WOODPECKER_AGENT_SECRET: ${WOODPECKER_AGENT_SECRET}
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
    depends_on:
      woodpecker-server:
        condition: service_started
    restart: unless-stopped
`,
	},

	// --- Automation --------------------------------------------------------
	{
		ID: "n8n", Name: "n8n", Description: "Workflow automation",
		Category: "Automation", ImageRef: "n8nio/n8n:latest", Port: 5678, DataMount: "/home/node/.n8n",
		Env:  "N8N_HOST={{HOST}}\nWEBHOOK_URL={{URL}}/\nN8N_PROXY_HOPS=1",
		Note: "The public address in the env below was filled in from the subdomain; change it there too if you change the subdomain.",
	},
	{
		ID: "node-red", Name: "Node-RED", Description: "Flow-based event wiring",
		Category: "Automation", ImageRef: "nodered/node-red:latest", Port: 1880, DataMount: "/data",
	},
	{
		ID: "home-assistant", Name: "Home Assistant", Description: "Home automation hub",
		Category: "Automation", ImageRef: "ghcr.io/home-assistant/home-assistant:stable", Port: 8123, DataMount: "/config",
		Note: "Runs on the bridge network here, so devices found by broadcast discovery will not appear; add them by IP.",
	},

	// --- Reading & RSS -----------------------------------------------------
	{
		ID: "freshrss", Name: "FreshRSS", Description: "Self-hosted feed reader",
		Category: "Reading & RSS", ImageRef: "freshrss/freshrss:latest", Port: 80, DataMount: "/var/www/FreshRSS/data",
		Env: "TZ=Etc/UTC\nCRON_MIN=*/20",
	},
	{
		ID: "miniflux", Name: "Miniflux", Description: "Minimalist feed reader",
		Category: "Reading & RSS", DeployType: "compose", Port: 8080, ComposeService: "miniflux",
		Env:  "DB_PASSWORD={{RANDOM}}\nADMIN_USERNAME=admin\nADMIN_PASSWORD={{RANDOM}}",
		Note: "The first account is created from ADMIN_USERNAME and ADMIN_PASSWORD below.",
		Compose: `services:
  miniflux:
    image: miniflux/miniflux:latest
    environment:
      DATABASE_URL: postgres://miniflux:${DB_PASSWORD}@db/miniflux?sslmode=disable
      RUN_MIGRATIONS: "1"
      CREATE_ADMIN: "1"
      ADMIN_USERNAME: ${ADMIN_USERNAME}
      ADMIN_PASSWORD: ${ADMIN_PASSWORD}
    depends_on:
      db:
        condition: service_healthy
    restart: unless-stopped

  db:
    image: postgres:16-alpine
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U $$POSTGRES_USER -d $$POSTGRES_DB"]
      interval: 5s
      timeout: 5s
      retries: 30
    environment:
      POSTGRES_USER: miniflux
      POSTGRES_PASSWORD: ${DB_PASSWORD}
      POSTGRES_DB: miniflux
    volumes:
      - ./data/db:/var/lib/postgresql/data
    restart: unless-stopped
`,
	},
	{
		ID: "wallabag", Name: "Wallabag", Description: "Read-it-later article archive",
		Category: "Reading & RSS", ImageRef: "wallabag/wallabag:latest", Port: 80, DataMount: "/var/www/wallabag/data",
		Env:  "SYMFONY__ENV__DOMAIN_NAME={{URL}}\nSYMFONY__ENV__SECRET={{RANDOM}}{{RANDOM}}",
		Note: "The public address in the env below was filled in from the subdomain; change it there too if you change the subdomain.",
	},
	{
		ID: "karakeep", Name: "Karakeep", Description: "Bookmark everything, searchable",
		Category: "Reading & RSS", DeployType: "compose", Port: 3000, ComposeService: "web",
		Env:  "NEXTAUTH_URL={{URL}}\nNEXTAUTH_SECRET={{RANDOM}}{{RANDOM}}\nMEILI_MASTER_KEY={{RANDOM}}{{RANDOM}}",
		Note: "The public address in the env below was filled in from the subdomain; change it there too if you change the subdomain.",
		Compose: `services:
  web:
    image: ghcr.io/karakeep-app/karakeep:release
    environment:
      DATA_DIR: /data
      NEXTAUTH_URL: ${NEXTAUTH_URL}
      NEXTAUTH_SECRET: ${NEXTAUTH_SECRET}
      MEILI_ADDR: http://meilisearch:7700
      MEILI_MASTER_KEY: ${MEILI_MASTER_KEY}
      BROWSER_WEB_URL: http://chrome:9222
    volumes:
      - ./data/karakeep:/data
    depends_on:
      meilisearch:
        condition: service_healthy
      chrome:
        condition: service_started
    restart: unless-stopped

  chrome:
    # Docker Hub, not the gcr.io mirror the upstream sample still names: that
    # project is gone and its registry answers 401 to an anonymous pull.
    image: zenika/alpine-chrome:123
    command:
      - --no-sandbox
      - --disable-gpu
      - --remote-debugging-address=0.0.0.0
      - --remote-debugging-port=9222
      - --hide-scrollbars
    restart: unless-stopped

  meilisearch:
    image: getmeili/meilisearch:v1.13.3
    healthcheck:
      test: ["CMD-SHELL", "curl -fsS http://127.0.0.1:7700/health || exit 1"]
      interval: 5s
      timeout: 5s
      retries: 30
    environment:
      MEILI_NO_ANALYTICS: "true"
      MEILI_MASTER_KEY: ${MEILI_MASTER_KEY}
    volumes:
      - ./data/meilisearch:/meili_data
    restart: unless-stopped
`,
	},

	// --- Analytics ---------------------------------------------------------
	{
		ID: "umami", Name: "Umami", Description: "Privacy-friendly web analytics",
		Category: "Analytics", DeployType: "compose", Port: 3000, ComposeService: "umami",
		Env: "DB_PASSWORD={{RANDOM}}\nAPP_SECRET={{RANDOM}}{{RANDOM}}",
		Compose: `services:
  umami:
    image: ghcr.io/umami-software/umami:postgresql-latest
    environment:
      DATABASE_URL: postgresql://umami:${DB_PASSWORD}@db:5432/umami
      DATABASE_TYPE: postgresql
      APP_SECRET: ${APP_SECRET}
    depends_on:
      db:
        condition: service_healthy
    restart: unless-stopped

  db:
    image: postgres:16-alpine
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U $$POSTGRES_USER -d $$POSTGRES_DB"]
      interval: 5s
      timeout: 5s
      retries: 30
    environment:
      POSTGRES_USER: umami
      POSTGRES_PASSWORD: ${DB_PASSWORD}
      POSTGRES_DB: umami
    volumes:
      - ./data/db:/var/lib/postgresql/data
    restart: unless-stopped
`,
	},
	{
		ID: "plausible", Name: "Plausible", Description: "Web analytics, community edition",
		Category: "Analytics", DeployType: "compose", Port: 8000, ComposeService: "plausible",
		Env:  "BASE_URL={{URL}}\nSECRET_KEY_BASE={{RANDOM}}{{RANDOM}}{{RANDOM}}{{RANDOM}}\nDB_PASSWORD={{RANDOM}}",
		Note: "The public address in the env below was filled in from the subdomain; change it there too if you change the subdomain.",
		Compose: `services:
  plausible:
    image: ghcr.io/plausible/community-edition:v3
    command: sh -c "/entrypoint.sh db createdb && /entrypoint.sh db migrate && /entrypoint.sh run"
    environment:
      BASE_URL: ${BASE_URL}
      SECRET_KEY_BASE: ${SECRET_KEY_BASE}
      DATABASE_URL: postgres://plausible:${DB_PASSWORD}@plausible-db:5432/plausible
      CLICKHOUSE_DATABASE_URL: http://plausible-events-db:8123/plausible_events_db
    depends_on:
      plausible-db:
        condition: service_healthy
      plausible-events-db:
        condition: service_healthy
    restart: unless-stopped

  plausible-db:
    image: postgres:16-alpine
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U $$POSTGRES_USER -d $$POSTGRES_DB"]
      interval: 5s
      timeout: 5s
      retries: 30
    environment:
      POSTGRES_USER: plausible
      POSTGRES_PASSWORD: ${DB_PASSWORD}
      POSTGRES_DB: plausible
    volumes:
      - ./data/db:/var/lib/postgresql/data
    restart: unless-stopped

  plausible-events-db:
    image: clickhouse/clickhouse-server:latest
    healthcheck:
      test: ["CMD-SHELL", "wget -qO- http://127.0.0.1:8123/ping || exit 1"]
      interval: 5s
      timeout: 5s
      retries: 30
    environment:
      CLICKHOUSE_SKIP_USER_SETUP: "1"
    volumes:
      - ./data/clickhouse:/var/lib/clickhouse
    ulimits:
      nofile:
        soft: 262144
        hard: 262144
    restart: unless-stopped
`,
	},
	{
		ID: "matomo", Name: "Matomo", Description: "Full-featured web analytics",
		Category: "Analytics", DeployType: "compose", Port: 80, ComposeService: "matomo",
		Env: "DB_PASSWORD={{RANDOM}}\nDB_ROOT_PASSWORD={{RANDOM}}",
		Compose: `services:
  matomo:
    image: matomo:apache
    environment:
      MATOMO_DATABASE_HOST: db
      MATOMO_DATABASE_USERNAME: matomo
      MATOMO_DATABASE_PASSWORD: ${DB_PASSWORD}
      MATOMO_DATABASE_DBNAME: matomo
    volumes:
      - ./data/matomo:/var/www/html
    depends_on:
      db:
        condition: service_healthy
    restart: unless-stopped

  db:
    image: mariadb:11
    healthcheck:
      test: ["CMD-SHELL", "mariadb-admin ping -h 127.0.0.1 -uroot -p$$MARIADB_ROOT_PASSWORD"]
      interval: 5s
      timeout: 5s
      retries: 30
    environment:
      MARIADB_DATABASE: matomo
      MARIADB_USER: matomo
      MARIADB_PASSWORD: ${DB_PASSWORD}
      MARIADB_ROOT_PASSWORD: ${DB_ROOT_PASSWORD}
    volumes:
      - ./data/db:/var/lib/mysql
    restart: unless-stopped
`,
	},

	// --- Websites ----------------------------------------------------------
	{
		ID: "ghost", Name: "Ghost", Description: "Publishing and newsletter platform",
		// A stack rather than a single image: Ghost 5 only supports MySQL in
		// production and, left alone, dials 127.0.0.1:3306 and shuts itself
		// down a second after reporting that the site is available.
		Category: "Websites", DeployType: "compose", Port: 2368, ComposeService: "ghost",
		Env:  "URL={{URL}}\nDB_PASSWORD={{RANDOM}}\nDB_ROOT_PASSWORD={{RANDOM}}",
		Note: "Ghost builds every link from the url in the env below, which was filled in from the subdomain; change it there too if you change the subdomain.",
		Compose: `services:
  ghost:
    image: ghost:5-alpine
    environment:
      url: ${URL}
      database__client: mysql
      database__connection__host: db
      database__connection__user: ghost
      database__connection__password: ${DB_PASSWORD}
      database__connection__database: ghost
    volumes:
      - ./data/content:/var/lib/ghost/content
    depends_on:
      db:
        condition: service_healthy
    restart: unless-stopped

  db:
    image: mysql:8
    healthcheck:
      test: ["CMD-SHELL", "mysqladmin ping -h 127.0.0.1 -uroot -p$$MYSQL_ROOT_PASSWORD"]
      interval: 5s
      timeout: 5s
      retries: 30
    environment:
      MYSQL_DATABASE: ghost
      MYSQL_USER: ghost
      MYSQL_PASSWORD: ${DB_PASSWORD}
      MYSQL_ROOT_PASSWORD: ${DB_ROOT_PASSWORD}
    volumes:
      - ./data/db:/var/lib/mysql
    restart: unless-stopped
`,
	},
	{
		ID: "wordpress", Name: "WordPress", Description: "The blogging and site platform",
		Category: "Websites", DeployType: "compose", Port: 80, ComposeService: "wordpress",
		Env: "DB_PASSWORD={{RANDOM}}\nDB_ROOT_PASSWORD={{RANDOM}}",
		Compose: `services:
  wordpress:
    image: wordpress:latest
    environment:
      WORDPRESS_DB_HOST: db
      WORDPRESS_DB_USER: wordpress
      WORDPRESS_DB_PASSWORD: ${DB_PASSWORD}
      WORDPRESS_DB_NAME: wordpress
    volumes:
      - ./data/html:/var/www/html
    depends_on:
      db:
        condition: service_healthy
    restart: unless-stopped

  db:
    image: mysql:8
    healthcheck:
      test: ["CMD-SHELL", "mysqladmin ping -h 127.0.0.1 -uroot -p$$MYSQL_ROOT_PASSWORD"]
      interval: 5s
      timeout: 5s
      retries: 30
    environment:
      MYSQL_DATABASE: wordpress
      MYSQL_USER: wordpress
      MYSQL_PASSWORD: ${DB_PASSWORD}
      MYSQL_ROOT_PASSWORD: ${DB_ROOT_PASSWORD}
    volumes:
      - ./data/db:/var/lib/mysql
    restart: unless-stopped
`,
	},
	{
		ID: "directus", Name: "Directus", Description: "Headless CMS over your own database",
		Category: "Websites", ImageRef: "directus/directus:latest", Port: 8055, DataMount: "/data",
		// Not /directus: that is where the application itself lives, and an
		// empty host directory bound over it hides cli.js, so the container
		// exits before it starts. Database and uploads are pointed into the
		// one directory Quasar does mount.
		Env: "KEY={{RANDOM}}\nSECRET={{RANDOM}}{{RANDOM}}\n" +
			"DB_CLIENT=sqlite3\nDB_FILENAME=/data/directus.db\nSTORAGE_LOCAL_ROOT=/data/uploads\n" +
			"ADMIN_EMAIL=admin@example.com\nADMIN_PASSWORD={{RANDOM}}",
	},

	// --- Utilities ---------------------------------------------------------
	{
		ID: "paperless-ngx", Name: "Paperless-ngx", Description: "Scan, index and search documents",
		Category: "Utilities", DeployType: "compose", Port: 8000, ComposeService: "webserver",
		Env:  "DB_PASSWORD={{RANDOM}}\nPAPERLESS_SECRET_KEY={{RANDOM}}{{RANDOM}}\nPAPERLESS_URL={{URL}}",
		Note: "Paperless rejects its own login form as a CSRF failure if PAPERLESS_URL is wrong; it was filled in from the subdomain, so update it if you change that.",
		Compose: `services:
  webserver:
    image: ghcr.io/paperless-ngx/paperless-ngx:latest
    environment:
      PAPERLESS_REDIS: redis://broker:6379
      PAPERLESS_DBHOST: db
      PAPERLESS_DBUSER: paperless
      PAPERLESS_DBPASS: ${DB_PASSWORD}
      PAPERLESS_DBNAME: paperless
      PAPERLESS_SECRET_KEY: ${PAPERLESS_SECRET_KEY}
      PAPERLESS_URL: ${PAPERLESS_URL}
    volumes:
      - ./data/data:/usr/src/paperless/data
      - ./data/media:/usr/src/paperless/media
      - ./data/export:/usr/src/paperless/export
      - ./data/consume:/usr/src/paperless/consume
    depends_on:
      db:
        condition: service_healthy
      broker:
        condition: service_healthy
    restart: unless-stopped

  broker:
    image: redis:7-alpine
    healthcheck:
      test: ["CMD", "redis-cli", "ping"]
      interval: 5s
      timeout: 5s
      retries: 30
    restart: unless-stopped

  db:
    image: postgres:16-alpine
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U $$POSTGRES_USER -d $$POSTGRES_DB"]
      interval: 5s
      timeout: 5s
      retries: 30
    environment:
      POSTGRES_USER: paperless
      POSTGRES_PASSWORD: ${DB_PASSWORD}
      POSTGRES_DB: paperless
    volumes:
      - ./data/db:/var/lib/postgresql/data
    restart: unless-stopped
`,
	},
	{
		ID: "stirling-pdf", Name: "Stirling PDF", Description: "Split, merge and convert PDFs",
		Category: "Utilities", ImageRef: "ghcr.io/stirling-tools/stirling-pdf:latest", Port: 8080, DataMount: "/configs",
	},
	{
		ID: "it-tools", Name: "IT Tools", Description: "Developer conversions and generators",
		Category: "Utilities", ImageRef: "corentinth/it-tools:latest", Port: 80,
	},
	{
		ID: "actual", Name: "Actual Budget", Description: "Envelope budgeting, local first",
		Category: "Utilities", ImageRef: "actualbudget/actual-server:latest", Port: 5006, DataMount: "/data",
	},
	{
		ID: "mealie", Name: "Mealie", Description: "Recipe manager and meal planner",
		Category: "Utilities", ImageRef: "ghcr.io/mealie-recipes/mealie:latest", Port: 9000, DataMount: "/app/data",
		Env: "ALLOW_SIGNUP=false\nBASE_URL={{URL}}",
	},

	// --- Databases ---------------------------------------------------------
	{
		ID: "postgres", Name: "PostgreSQL 17", Description: "Relational database",
		Category: "Databases", ImageRef: "postgres:17-alpine", Port: 5432, DataMount: "/var/lib/postgresql/data",
		Env: "POSTGRES_USER=app\nPOSTGRES_PASSWORD={{RANDOM}}\nPOSTGRES_DB=app",
		Raw: true,
	},
	{
		ID: "mysql", Name: "MySQL 8", Description: "Relational database",
		Category: "Databases", ImageRef: "mysql:8", Port: 3306, DataMount: "/var/lib/mysql",
		Env: "MYSQL_ROOT_PASSWORD={{RANDOM}}\nMYSQL_DATABASE=app\nMYSQL_USER=app\nMYSQL_PASSWORD={{RANDOM}}",
		Raw: true,
	},
	{
		ID: "mariadb", Name: "MariaDB 11", Description: "Relational database",
		Category: "Databases", ImageRef: "mariadb:11", Port: 3306, DataMount: "/var/lib/mysql",
		Env: "MARIADB_ROOT_PASSWORD={{RANDOM}}\nMARIADB_DATABASE=app\nMARIADB_USER=app\nMARIADB_PASSWORD={{RANDOM}}",
		Raw: true,
	},
	{
		ID: "redis", Name: "Redis 7", Description: "In-memory data store",
		Category: "Databases", ImageRef: "redis:7-alpine", Port: 6379, DataMount: "/data",
		Raw: true,
	},
	{
		ID: "mongo", Name: "MongoDB 7", Description: "Document database",
		Category: "Databases", ImageRef: "mongo:7", Port: 27017, DataMount: "/data/db",
		Env: "MONGO_INITDB_ROOT_USERNAME=root\nMONGO_INITDB_ROOT_PASSWORD={{RANDOM}}",
		Raw: true,
	},

	// --- Game servers ------------------------------------------------------
	//
	// These publish their own host ports, which Quasar's compose adaptation
	// leaves alone as long as they are not 80 or 443. Change the host side of
	// a ports entry if two servers want the same number.
	{
		ID: "minecraft", Name: "Minecraft (Java)", Description: "Vanilla or modded Java server",
		Category: "Game servers", DeployType: "compose", Port: 25565, ComposeService: "minecraft",
		Env: "MINECRAFT_VERSION=LATEST\nMEMORY=2G",
		Raw: true, Note: "Default port 25565/tcp.",
		Compose: `services:
  minecraft:
    image: itzg/minecraft-server:latest
    ports:
      - "25565:25565"
    environment:
      EULA: "TRUE"
      VERSION: ${MINECRAFT_VERSION}
      MEMORY: ${MEMORY}
    volumes:
      - ./data:/data
    restart: unless-stopped
`,
	},
	{
		ID: "valheim", Name: "Valheim", Description: "Dedicated Valheim world",
		Category: "Game servers", DeployType: "compose", Port: 2456, ComposeService: "valheim",
		Env: "SERVER_NAME=Quasar\nWORLD_NAME=Dedicated\nSERVER_PASS={{RANDOM}}",
		Raw: true, Note: "Ports 2456-2458/udp.",
		Compose: `services:
  valheim:
    image: lloesche/valheim-server:latest
    ports:
      - "2456-2458:2456-2458/udp"
    environment:
      SERVER_NAME: ${SERVER_NAME}
      WORLD_NAME: ${WORLD_NAME}
      SERVER_PASS: ${SERVER_PASS}
      SERVER_PUBLIC: "false"
    volumes:
      - ./data/config:/config
      - ./data/server:/opt/valheim
    restart: unless-stopped
`,
	},
	{
		ID: "palworld", Name: "Palworld", Description: "Dedicated Palworld server",
		Category: "Game servers", DeployType: "compose", Port: 8211, ComposeService: "palworld",
		Env: "SERVER_NAME=Quasar\nSERVER_PASSWORD={{RANDOM}}\nADMIN_PASSWORD={{RANDOM}}\nPLAYERS=16",
		Raw: true, Note: "Port 8211/udp.",
		Compose: `services:
  palworld:
    image: thijsvanloef/palworld-server-docker:latest
    ports:
      - "8211:8211/udp"
    environment:
      PUID: 1000
      PGID: 1000
      PORT: 8211
      PLAYERS: ${PLAYERS}
      SERVER_NAME: ${SERVER_NAME}
      SERVER_PASSWORD: ${SERVER_PASSWORD}
      ADMIN_PASSWORD: ${ADMIN_PASSWORD}
    volumes:
      - ./data:/palworld
    restart: unless-stopped
`,
	},
	{
		ID: "satisfactory", Name: "Satisfactory", Description: "Dedicated Satisfactory server",
		Category: "Game servers", DeployType: "compose", Port: 7777, ComposeService: "satisfactory",
		Raw: true, Note: "Port 7777/udp and 7777/tcp.",
		Compose: `services:
  satisfactory:
    image: wolveix/satisfactory-server:latest
    ports:
      - "7777:7777/udp"
      - "7777:7777/tcp"
    environment:
      MAXPLAYERS: 4
      PGID: 1000
      PUID: 1000
      STEAMBETA: "false"
    volumes:
      - ./data:/config
    restart: unless-stopped
`,
	},
	{
		ID: "terraria", Name: "Terraria", Description: "Dedicated Terraria world",
		Category: "Game servers", DeployType: "compose", Port: 7777, ComposeService: "terraria",
		Env: "WORLD_SIZE=2\nWORLD_NAME=Quasar\nDIFFICULTY=1\nMAX_PLAYERS=8",
		Raw: true, Note: "Port 7777/tcp.",
		// No WORLD_FILENAME: naming a world file puts the image on the branch
		// that expects it to exist already, and it restart-loops on a fresh
		// install complaining that -autocreate was not set. WORLDNAME with
		// AUTOCREATE is the branch that generates one.
		Compose: `services:
  terraria:
    image: ryshe/terraria:latest
    ports:
      - "7777:7777"
    environment:
      AUTOCREATE: ${WORLD_SIZE}
      WORLDNAME: ${WORLD_NAME}
      DIFFICULTY: ${DIFFICULTY}
      MAXPLAYERS: ${MAX_PLAYERS}
    volumes:
      - ./data:/root/.local/share/Terraria/Worlds
    stdin_open: true
    tty: true
    restart: unless-stopped
`,
	},
	{
		ID: "factorio", Name: "Factorio", Description: "Dedicated Factorio server",
		Category: "Game servers", DeployType: "compose", Port: 34197, ComposeService: "factorio",
		Raw: true, Note: "Port 34197/udp.",
		Compose: `services:
  factorio:
    image: factoriotools/factorio:stable
    ports:
      - "34197:34197/udp"
    environment:
      UPDATE_MODS_ON_START: "false"
    volumes:
      - ./data:/factorio
    restart: unless-stopped
`,
	},
}

// Group is one category with the templates filed under it.
type Group struct {
	Category  string
	Templates []Template
}

// Grouped returns the templates by category, in Categories order. A category
// with no entries is left out, and a template filed under an unknown category
// would vanish — the test in this package guards against that.
func Grouped() []Group {
	var out []Group
	for _, c := range Categories {
		var in []Template
		for _, t := range Templates {
			if t.Category == c {
				in = append(in, t)
			}
		}
		if len(in) > 0 {
			out = append(out, Group{Category: c, Templates: in})
		}
	}
	return out
}

// Get returns the template with the given ID, or nil.
func Get(id string) *Template {
	for i := range Templates {
		if Templates[i].ID == id {
			return &Templates[i]
		}
	}
	return nil
}

// RenderEnv resolves an entry's placeholders for the app about to be created:
// {{RANDOM}} becomes a fresh secret, {{HOST}} the app's public hostname and
// {{URL}} its https address.
//
// Filling the address in matters more than it looks. A dozen of these refuse to
// work until they are told the URL they are served on — Outline will not start,
// Paperless rejects its own login form as a CSRF failure, Ghost builds every
// link from it — and Quasar already knows the answer, so asking the operator to
// paste it back in only creates a way to get it wrong. An empty host leaves the
// placeholders in place rather than writing a URL with a hole in it.
func (t *Template) RenderEnv(host string) string {
	env := t.Env
	for strings.Contains(env, "{{RANDOM}}") {
		buf := make([]byte, 16)
		rand.Read(buf)
		env = strings.Replace(env, "{{RANDOM}}", hex.EncodeToString(buf), 1)
	}
	if host != "" {
		env = strings.ReplaceAll(env, "{{HOST}}", host)
		env = strings.ReplaceAll(env, "{{URL}}", "https://"+host)
	}
	return env
}
