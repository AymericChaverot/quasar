// Package docker orchestrates application containers through the Docker
// Engine API, reached via the tecnativa/docker-socket-proxy (DOCKER_HOST).
package docker

import (
	"crypto/rand"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/client"

	"quasar/internal/config"
	"quasar/internal/db"
)

type Client struct {
	api       *client.Client
	dbc       *sql.DB
	domain    string
	network   string
	socketNet string
	appsDir   string

	mu      sync.Mutex
	deploys map[string]*DeployState // app ID -> in-flight/last deploy state
}

// DeployState tracks an asynchronous deploy so the UI can show progress.
type DeployState struct {
	Running bool
	Err     string
}

func New(cfg config.Config, database *sql.DB) (*Client, error) {
	api, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, err
	}
	return &Client{
		api:       api,
		dbc:       database,
		domain:    cfg.Domain,
		network:   cfg.TraefikNetwork,
		socketNet: cfg.SocketNetwork,
		appsDir:   cfg.AppsDir,
		deploys:   map[string]*DeployState{},
	}, nil
}

// appLabel marks a container as the managed container of a single-container
// app. Container names carry a per-deploy suffix so a new container can be
// started before the old one is retired, so this label — not the name — is
// what identifies an app's container.
const appLabel = "quasar.app"

// ContainerName returns the stable network alias every one of an app's
// containers answers to, whichever deploy created it. Anything addressing an
// app over the Docker network (the health checker, other apps) uses this name;
// during a deploy Docker's DNS round-robins it across old and new.
func ContainerName(appID string) string { return "qs-" + appID }

// deployContainerName returns the actual container name for one deploy. The
// suffix is what lets the replacement container exist alongside the container
// it replaces, since Docker names are unique. It carries a timestamp to stay
// readable in `docker ps` plus random bytes for the uniqueness, because clock
// resolution alone is not fine enough to separate two quick deploys.
func deployContainerName(appID string) string {
	var b [4]byte
	rand.Read(b[:])
	return fmt.Sprintf("qs-%s-%d-%x", appID, time.Now().Unix(), b)
}

// composeProject returns the docker compose project name for a compose app.
func composeProject(appID string) string { return "qs-" + appID }

// buildTagPrefix is the local image repository for an app's git builds.
func buildTagPrefix(appID string) string { return "qs-" + appID }

func (c *Client) AppDir(appID string) string  { return filepath.Join(c.appsDir, appID) }
func (c *Client) DataDir(appID string) string { return filepath.Join(c.appsDir, appID, "data") }

// hostPath converts an in-container apps path to the equivalent host path.
// Both are identical because /opt/quasar/apps is mounted at the same path.
func (c *Client) hostDataDir(appID string) string {
	return c.appsDir + "/" + appID + "/data"
}

// EnsureAppDirs creates the on-disk layout for an app and writes its .env file.
func (c *Client) EnsureAppDirs(a *db.App) error {
	for _, dir := range []string{c.AppDir(a.ID), c.DataDir(a.ID)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return c.WriteEnvFile(a)
}

// WriteEnvFile persists the app's env vars to apps/<id>/.env.
func (c *Client) WriteEnvFile(a *db.App) error {
	return os.WriteFile(filepath.Join(c.AppDir(a.ID), ".env"), []byte(a.EnvContent+"\n"), 0o600)
}

// AppDirSize sums the on-disk size of an app's directory.
func (c *Client) AppDirSize(appID string) int64 {
	var size int64
	filepath.Walk(c.AppDir(appID), func(_ string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			size += info.Size()
		}
		return nil
	})
	return size
}

// traefikLabels builds the labels that make Traefik route the app's
// subdomain — plus any custom domains — to the container's internal port.
func (c *Client) traefikLabels(a *db.App) map[string]string {
	return c.traefikLabelsPort(a, a.Port)
}

// traefikLabelsPort is traefikLabels routed at a port other than the app's own.
// A stack serves from whichever port its front-end service listens on, which is
// written in its compose file rather than in the app's configuration.
func (c *Client) traefikLabelsPort(a *db.App, port int) map[string]string {
	r := "qs-" + a.ID
	host := a.Subdomain + "." + c.domain
	if a.Subdomain == "@" { // app claims the root domain
		host = c.domain
	}
	rule := fmt.Sprintf("Host(`%s`)", host)
	for _, d := range a.CustomDomainList() {
		rule += fmt.Sprintf(" || Host(`%s`)", d)
	}
	labels := map[string]string{
		appLabel:         a.ID,
		"traefik.enable": "true",
		fmt.Sprintf("traefik.http.routers.%s.rule", r):                      rule,
		fmt.Sprintf("traefik.http.routers.%s.entrypoints", r):               "websecure",
		fmt.Sprintf("traefik.http.routers.%s.tls.certresolver", r):          "letsencrypt",
		fmt.Sprintf("traefik.http.services.%s.loadbalancer.server.port", r): fmt.Sprintf("%d", port),
	}
	// Old and new containers share this router for the few seconds a deploy
	// overlaps them, so Traefik sees one service with two servers. Its own
	// health check is what keeps requests off the one that isn't serving yet.
	if a.HealthPath != "" {
		labels[fmt.Sprintf("traefik.http.services.%s.loadbalancer.healthcheck.path", r)] = a.HealthPath
		labels[fmt.Sprintf("traefik.http.services.%s.loadbalancer.healthcheck.interval", r)] = "5s"
		labels[fmt.Sprintf("traefik.http.services.%s.loadbalancer.healthcheck.timeout", r)] = "3s"
	}
	// Middlewares are collected in order and attached to the router as one
	// chain. Traefik applies them left to right, so the cheapest rejections
	// come first: an blocked address never costs a rate-limit slot or a bcrypt
	// comparison.
	var chain []string

	if list := a.IPAllowList(); len(list) > 0 {
		labels[fmt.Sprintf("traefik.http.middlewares.%s-ipallow.ipallowlist.sourcerange", r)] = strings.Join(list, ",")
		chain = append(chain, r+"-ipallow")
	}
	if a.RateLimit > 0 {
		labels[fmt.Sprintf("traefik.http.middlewares.%s-ratelimit.ratelimit.average", r)] = fmt.Sprintf("%d", a.RateLimit)
		// Burst absorbs the bunched requests a single page load makes; without
		// headroom a normal visit trips its own limit.
		labels[fmt.Sprintf("traefik.http.middlewares.%s-ratelimit.ratelimit.burst", r)] = fmt.Sprintf("%d", a.RateLimit*3)
		chain = append(chain, r+"-ratelimit")
	}
	if a.BasicAuthUser != "" && a.BasicAuthHash != "" {
		labels[fmt.Sprintf("traefik.http.middlewares.%s-auth.basicauth.users", r)] = a.BasicAuthUser + ":" + a.BasicAuthHash
		chain = append(chain, r+"-auth")
	}
	if a.SecurityHeaders {
		m := fmt.Sprintf("traefik.http.middlewares.%s-headers.headers.", r)
		labels[m+"stsSeconds"] = "31536000"
		labels[m+"stsIncludeSubdomains"] = "true"
		labels[m+"contentTypeNosniff"] = "true"
		labels[m+"browserXssFilter"] = "true"
		labels[m+"referrerPolicy"] = "strict-origin-when-cross-origin"
		labels[m+"frameDeny"] = "true"
		chain = append(chain, r+"-headers")
	}
	if len(chain) > 0 {
		labels[fmt.Sprintf("traefik.http.routers.%s.middlewares", r)] = strings.Join(chain, ",")
	}
	return labels
}

// refRegistry extracts the registry host from an image reference
// ("ghcr.io/org/app:v1" -> "ghcr.io", "nginx:latest" -> "docker.io").
func refRegistry(ref string) string {
	host, rest, ok := strings.Cut(ref, "/")
	if !ok || rest == "" {
		return "docker.io"
	}
	if strings.ContainsAny(host, ".:") || host == "localhost" {
		return host
	}
	return "docker.io"
}

func (c *Client) setDeploy(appID string, running bool, errMsg string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.deploys[appID] = &DeployState{Running: running, Err: errMsg}
}

// Deploying returns the deploy state for an app (nil if never deployed this boot).
func (c *Client) Deploying(appID string) *DeployState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.deploys[appID]
}

func (c *Client) forgetDeploy(appID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.deploys, appID)
}
