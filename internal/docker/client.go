// Package docker orchestrates application containers through the Docker
// Engine API, reached via the tecnativa/docker-socket-proxy (DOCKER_HOST).
package docker

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/docker/docker/client"

	"quasar/internal/config"
	"quasar/internal/db"
)

type Client struct {
	api     *client.Client
	dbc     *sql.DB
	domain  string
	network string
	appsDir string

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
		api:     api,
		dbc:     database,
		domain:  cfg.Domain,
		network: cfg.TraefikNetwork,
		appsDir: cfg.AppsDir,
		deploys: map[string]*DeployState{},
	}, nil
}

// ContainerName returns the managed container name for a single-container app.
func ContainerName(appID string) string { return "qs-" + appID }

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
	r := "qs-" + a.ID
	rule := fmt.Sprintf("Host(`%s.%s`)", a.Subdomain, c.domain)
	for _, d := range a.CustomDomainList() {
		rule += fmt.Sprintf(" || Host(`%s`)", d)
	}
	labels := map[string]string{
		"quasar.app":     a.ID,
		"traefik.enable": "true",
		fmt.Sprintf("traefik.http.routers.%s.rule", r):                      rule,
		fmt.Sprintf("traefik.http.routers.%s.entrypoints", r):               "websecure",
		fmt.Sprintf("traefik.http.routers.%s.tls.certresolver", r):          "letsencrypt",
		fmt.Sprintf("traefik.http.services.%s.loadbalancer.server.port", r): fmt.Sprintf("%d", a.Port),
	}
	if a.BasicAuthUser != "" && a.BasicAuthHash != "" {
		labels[fmt.Sprintf("traefik.http.middlewares.%s-auth.basicauth.users", r)] = a.BasicAuthUser + ":" + a.BasicAuthHash
		labels[fmt.Sprintf("traefik.http.routers.%s.middlewares", r)] = r + "-auth"
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
