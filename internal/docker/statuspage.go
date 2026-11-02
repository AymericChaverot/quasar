package docker

import (
	"context"
	"fmt"
	"github.com/moby/moby/client"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
)

// The status page runs in a container of its own, started from the very
// image the dashboard runs, in the binary's status-page mode. It is given the
// address of the status API and the key to read it with, and nothing else: no
// volume, no socket, a read-only filesystem, no capabilities, an unprivileged
// user and a little memory. Traefik routes its subdomain to it.

// StatusPageName is the page's container.
const StatusPageName = "quasar-status"

// dashboardName is the dashboard's own container, whose image the page runs.
const dashboardName = "quasar-dashboard"

const statusPageLabel = "quasar.status-page"

// StatusPage is what the dashboard knows of the page's container.
type StatusPage struct {
	// State is Docker's word for it, or "not deployed".
	State string
	// Current is whether it runs the dashboard's own image: after an update
	// it does not, until it is deployed again.
	Current bool
}

// dashboardImage is the image the dashboard's container was started from, by
// id, so the page is started from exactly that and never pulls anything.
func (c *Client) dashboardImage(ctx context.Context) (string, error) {
	info, err := c.inspect(ctx, dashboardName)
	if err != nil {
		return "", fmt.Errorf("the dashboard does not run in a container named %s, so there is no image to start the page from: %w", dashboardName, err)
	}
	return info.Image, nil
}

// StatusPageState reports the page's container.
func (c *Client) StatusPageState(ctx context.Context) StatusPage {
	info, err := c.inspect(ctx, StatusPageName)
	if err != nil {
		return StatusPage{State: "not deployed"}
	}
	st := StatusPage{State: string(info.State.Status)}
	if img, err := c.dashboardImage(ctx); err == nil {
		st.Current = img == info.Image
	}
	return st
}

// DeployStatusPage (re)creates the page's container on host, reading the
// status at apiURL with key.
func (c *Client) DeployStatusPage(ctx context.Context, host, apiURL, key string) error {
	img, err := c.dashboardImage(ctx)
	if err != nil {
		return err
	}
	c.removeContainer(ctx, StatusPageName)

	created, err := c.create(ctx,
		&container.Config{
			Image: img,
			Cmd:   []string{"status-page"},
			Env: []string{
				"QUASAR_STATUS_API=" + apiURL,
				"QUASAR_STATUS_KEY=" + key,
			},
			User:   "65534:65534",
			Labels: statusPageLabels(host, c.network),
		},
		&container.HostConfig{
			RestartPolicy:  container.RestartPolicy{Name: container.RestartPolicyUnlessStopped},
			ReadonlyRootfs: true,
			CapDrop:        []string{"ALL"},
			SecurityOpt:    []string{"no-new-privileges:true"},
			Resources: container.Resources{
				Memory:    64 << 20,
				PidsLimit: new(int64(64)),
			},
		},
		&network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{c.network: {}},
		},
		StatusPageName)
	if err != nil {
		return fmt.Errorf("create the status page: %w", err)
	}
	if _, err := c.api.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		c.removeContainer(ctx, StatusPageName)
		return fmt.Errorf("start the status page: %w", err)
	}
	return nil
}

// RemoveStatusPage removes the page's container, if there is one.
func (c *Client) RemoveStatusPage(ctx context.Context) {
	c.removeContainer(ctx, StatusPageName)
}

// statusPageLabels routes host to the page, over HTTPS with a certificate of
// its own and the headers any public page should carry.
func statusPageLabels(host, net string) map[string]string {
	const r = "quasar-status"
	m := "traefik.http.middlewares." + r + "-headers.headers."
	return map[string]string{
		statusPageLabel:                                            "true",
		"traefik.enable":                                           "true",
		"traefik.docker.network":                                   net,
		"traefik.http.routers." + r + ".rule":                      fmt.Sprintf("Host(`%s`)", host),
		"traefik.http.routers." + r + ".entrypoints":               "websecure",
		"traefik.http.routers." + r + ".tls.certresolver":          "letsencrypt",
		"traefik.http.routers." + r + ".middlewares":               r + "-headers",
		"traefik.http.services." + r + ".loadbalancer.server.port": "8080",
		m + "stsSeconds":                                           "31536000",
		m + "contentTypeNosniff":                                   "true",
		m + "referrerPolicy":                                       "no-referrer",
		m + "frameDeny":                                            "true",
	}
}
