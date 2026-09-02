// Package docker orchestrates application containers through the Docker
// Engine API, reached via the tecnativa/docker-socket-proxy (DOCKER_HOST).
package docker

import (
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/client"

	"quasar/internal/config"
	"quasar/internal/db"
	"quasar/internal/secrets"
)

type Client struct {
	api *client.Client
	dbc *sql.DB
	// keyring opens the git credentials, which are the one secret this package
	// reads for itself; an app's env and compose data arrive already decrypted.
	keyring   *secrets.Keyring
	domain    string
	network   string
	socketNet string
	appsDir   string
	// edgeAuthURL is where Traefik calls this dashboard back to have a request
	// to a protected app authorised. It is written into the app's labels, so it
	// has to be an address Traefik itself resolves on the internal network.
	edgeAuthURL string

	// The git credential helper is written once and reused; it carries no
	// secret of its own, only the code that reads one from the environment.
	askPassOnce sync.Once
	askPassPath string
	askPassErr  error

	mu   sync.Mutex
	runs map[string]*deployRun // app ID -> in-flight/last deploy
}

// DeployState is the part of a deploy the status panel and the API care about:
// whether one is running, and what the last one ended as. Everything else it
// recorded — the output, the step, the percentage — is read through
// WatchDeploy.
type DeployState struct {
	Running bool
	Err     string
}

func New(cfg config.Config, database *sql.DB, keyring *secrets.Keyring) (*Client, error) {
	api, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, err
	}
	return &Client{
		api:         api,
		dbc:         database,
		keyring:     keyring,
		domain:      cfg.Domain,
		network:     cfg.TraefikNetwork,
		socketNet:   cfg.SocketNetwork,
		appsDir:     cfg.AppsDir,
		edgeAuthURL: cfg.EdgeAuthURL,
		runs:        map[string]*deployRun{},
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
	// Errors are absorbed by the callback below, so the size is a best effort
	// by construction: an unreadable entry contributes nothing rather than
	// failing a page that is only showing a number.
	//
	// WalkDir rather than Walk: Walk stats every entry to hand the callback a
	// FileInfo, including the directories, and a data directory is mostly
	// directories by count. WalkDir reads them from the directory listing and
	// only stats what is asked about, which here is the files alone.
	_ = filepath.WalkDir(c.AppDir(appID), func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			size += info.Size()
		}
		return nil
	})
	return size
}

// routerName is the Traefik router an app is served by, and the prefix its
// middlewares are named with.
func routerName(appID string) string { return "qs-" + appID }

// edgeAuthLabel is the label pointing an app's protection at the dashboard.
func edgeAuthLabel(appID string) string {
	return fmt.Sprintf("traefik.http.middlewares.%s-auth.forwardauth.address", routerName(appID))
}

// ProtectionPending reports whether an app's password protection has been
// turned on or off since the container serving it was created.
//
// Only that much is a redeploy's business. The credentials themselves are read
// from the database on every request, so a changed password takes effect at
// once; what is baked into the container is whether the router carries the
// middleware at all, and that is what a deploy has to catch up with. An app
// with no container has nothing to disagree with, so nothing is pending.
func (c *Client) ProtectionPending(ctx context.Context, a *db.App) bool {
	labels, ok := c.servingLabels(ctx, a)
	return ok && !protectionApplied(a, labels)
}

// protectionApplied reports whether a container created with these labels
// carries the protection the app is configured with — neither side having any
// counting as agreement.
func protectionApplied(a *db.App, labels map[string]string) bool {
	return (labels[edgeAuthLabel(a.ID)] != "") == protected(a)
}

// protected reports whether an app has password protection configured.
func protected(a *db.App) bool { return a.BasicAuthUser != "" && a.BasicAuthHash != "" }

// servingLabels returns the labels on the container that serves the app — a
// stack's front end, or the newest container of a single-container app — and
// false when it has none. A stopped container still answers: it is what a
// start, as opposed to a deploy, would put back in front of visitors.
func (c *Client) servingLabels(ctx context.Context, a *db.App) (map[string]string, bool) {
	if c.UsesCompose(a) {
		ct, ok := c.composeWebContainer(ctx, a.ID)
		return ct.Labels, ok
	}
	list := c.appContainers(ctx, a.ID)
	if len(list) == 0 {
		return nil, false
	}
	return list[0].Labels, true
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
	r := routerName(a.ID)
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
	// Password protection is delegated to the dashboard rather than checked by
	// Traefik itself, which is what allows a visitor to be shown a page instead
	// of the browser's own credentials box: that box is the browser's answer to
	// the WWW-Authenticate header Traefik's basicauth middleware sends, and
	// nothing about it can be styled or explained. The dashboard answers this
	// call with the login page, and only lets the request through once it has
	// seen credentials it accepts.
	if protected(a) {
		labels[edgeAuthLabel(a.ID)] = c.edgeAuthURL + "/edge-auth/" + a.ID
		// Left unset, Traefik strips some X-Forwarded headers a client sent and
		// passes others through untouched — and says so in a warning at every
		// start. False is the answer that matches what the dashboard reads:
		// the address a request came from, and the host it asked for, are only
		// worth anything if Traefik is the one that wrote them.
		labels[fmt.Sprintf("traefik.http.middlewares.%s-auth.forwardauth.trustForwardHeader", r)] = "false"
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

// Deploying returns the deploy state for an app (nil if never deployed this
// boot). A record exists as soon as anything watches the app, so what answers
// "never deployed" is the generation rather than the record's absence.
func (c *Client) Deploying(appID string) *DeployState {
	c.mu.Lock()
	r := c.runs[appID]
	c.mu.Unlock()
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.gen == 0 {
		return nil
	}
	return &DeployState{Running: r.running, Err: r.err}
}

func (c *Client) forgetDeploy(appID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.runs, appID)
}
