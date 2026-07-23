package docker

import (
	"context"
	"fmt"
	"io"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
)

// SelfUpdate pulls the given dashboard image and spawns a short-lived
// "updater" container that recreates the dashboard service. The dashboard
// cannot recreate its own container (the command would die with it), so the
// updater runs independently:
//
//  1. pin QUASAR_IMAGE in /opt/quasar/.env to the new image
//  2. docker compose up -d --no-deps dashboard  (recreates only the
//     dashboard; Traefik and every app container keep running untouched)
//
// --no-deps is load-bearing, not an optimization: without it, compose also
// evaluates dashboard's depends_on (socket-proxy, traefik) and recreates any
// of them whose config hash has drifted from docker-compose.yml on disk
// (e.g. after a `git pull` that changed the socket-proxy service). The
// updater's only Docker API access is DOCKER_HOST=tcp://socket-proxy:2375,
// so recreating socket-proxy means stopping it over a connection routed
// through itself — the stop request's connection dies mid-flight (EOF) and
// the update fails, potentially leaving socket-proxy down for the whole
// platform. Infrastructure containers are only ever meant to be recreated by
// an operator running `docker compose up -d` directly on the host.
//
// The updater uses the freshly pulled image itself, which ships docker-cli
// and the compose plugin.
func (c *Client) SelfUpdate(ctx context.Context, imageRef, socketNetwork string) error {
	rc, err := c.api.ImagePull(ctx, imageRef, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("pull %s: %w", imageRef, err)
	}
	io.Copy(io.Discard, rc)
	rc.Close()

	const updaterName = "quasar-updater"
	c.removeContainer(ctx, updaterName)

	script := fmt.Sprintf(
		`grep -q '^QUASAR_IMAGE=' /opt/quasar/.env && sed -i 's|^QUASAR_IMAGE=.*|QUASAR_IMAGE=%[1]s|' /opt/quasar/.env || echo 'QUASAR_IMAGE=%[1]s' >> /opt/quasar/.env; cd /opt/quasar && docker compose up -d --no-build --no-deps dashboard`,
		imageRef)

	created, err := c.api.ContainerCreate(ctx,
		&container.Config{
			Image:      imageRef,
			Entrypoint: []string{"/bin/sh", "-c"},
			Cmd:        []string{script},
			Env:        []string{"DOCKER_HOST=tcp://socket-proxy:2375"},
			Labels:     map[string]string{"quasar.updater": "true"},
		},
		&container.HostConfig{
			// Not auto-removed: the dashboard can't wait for this container
			// (it dies mid-recreation), so `docker logs quasar-updater` is
			// the only way to diagnose a failed update after the fact. The
			// next SelfUpdate run removes the previous one (see above).
			Binds: []string{"/opt/quasar:/opt/quasar"},
		},
		&network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{socketNetwork: {}},
		},
		nil, updaterName)
	if err != nil {
		return fmt.Errorf("create updater: %w", err)
	}
	if err := c.api.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		return fmt.Errorf("start updater: %w", err)
	}
	return nil
}
