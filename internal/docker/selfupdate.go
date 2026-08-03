package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
)

// updaterScript pins the new image and recreates the dashboard, and guarantees
// that a dashboard container is running by the time it exits — rolling back to
// the previously pinned image if the new one will not come up. __IMAGE__ is
// substituted before the script is handed to the updater container.
//
// That guarantee is the whole point: compose recreating a container is not
// atomic, it removes the old container before creating the new one. Anything
// that interrupts the updater inside that window — the OOM killer on a small
// VPS, a socket-proxy hiccup, an image that starts and immediately crashes —
// leaves the dashboard with no container at all, and `restart: unless-stopped`
// cannot bring back a container that no longer exists. A single `docker
// compose up -d` with no verification is why a failed update ends with the
// operator having to SSH in and start the stack by hand.
//
// --no-deps is load-bearing, not an optimization: without it, compose also
// evaluates dashboard's depends_on (socket-proxy, traefik) and recreates any
// of them whose config hash has drifted from docker-compose.yml on disk (e.g.
// after a `git pull` that changed the socket-proxy service). The updater's
// only Docker API access is DOCKER_HOST=tcp://socket-proxy:2375, so recreating
// socket-proxy means stopping it over a connection routed through itself — the
// stop request's connection dies mid-flight (EOF) and the update fails,
// potentially leaving socket-proxy down for the whole platform. Infrastructure
// containers are only ever meant to be recreated by an operator running
// `docker compose up -d` directly on the host.
const updaterScript = `
set -u

# Persisted under storage/ (a mounted volume) rather than left in this
# container's log: the dashboard can serve it, and it survives the next
# SelfUpdate removing this container.
exec >>/opt/quasar/storage/update.log 2>&1
echo "=== $(date -u '+%Y-%m-%dT%H:%M:%SZ') self-update to __IMAGE__ ==="

cd /opt/quasar || { echo "/opt/quasar not mounted"; exit 1; }

# Whatever the dashboard is running right now is, by definition, an image that
# boots on this host. Empty means the install never pinned one and falls back
# to the default in docker-compose.yml.
PREV=$(sed -n 's/^QUASAR_IMAGE=//p' .env | tail -n 1)

pin() {
	if grep -q '^QUASAR_IMAGE=' .env; then
		sed -i "s|^QUASAR_IMAGE=.*|QUASAR_IMAGE=$1|" .env
	else
		# A hand-edited .env may lack its trailing newline, which would glue
		# the pin onto whatever the last setting is.
		[ -n "$(tail -c 1 .env)" ] && echo >>.env
		echo "QUASAR_IMAGE=$1" >>.env
	fi
	# sed -i replaces the inode, so the 0600 mode setup.sh set is lost.
	chmod 600 .env
}

unpin() { sed -i '/^QUASAR_IMAGE=/d' .env; chmod 600 .env; }

up() { docker compose up -d --no-build --no-deps dashboard; }

# Running now and still running five seconds later, so a container that boots
# and immediately crash-loops is not mistaken for a successful update.
stable() {
	docker inspect -f '{{.State.Running}}' quasar-dashboard 2>/dev/null | grep -qx true || return 1
	sleep 5
	docker inspect -f '{{.State.Running}}' quasar-dashboard 2>/dev/null | grep -qx true
}

pin __IMAGE__
n=0
while [ "$n" -lt 3 ]; do
	up || true
	if stable; then
		echo "dashboard running on __IMAGE__"
		exit 0
	fi
	n=$((n + 1))
	echo "attempt $n did not bring the dashboard up"
	sleep 3
done

echo "__IMAGE__ will not stay up, rolling back to ${PREV:-the compose default}"
if [ -n "$PREV" ]; then pin "$PREV"; else unpin; fi
up || true
if stable; then
	echo "rolled back to ${PREV:-the compose default}"
	exit 0
fi

# Exit non-zero so this container's on-failure restart policy runs the whole
# thing again: a dashboard that is down is the one outcome worth retrying.
echo "rollback failed, dashboard is DOWN"
exit 1
`

// PullStatus is how far an image pull has got. Total stays zero until the
// daemon has announced the layers it is about to transfer, so Percent is only
// meaningful once it isn't — a pull reports nothing for the first second or two
// while the manifest is resolved.
type PullStatus struct {
	Current int64   // bytes received across every layer
	Total   int64   // bytes the layers add up to
	Percent float64 // 0 to 100, zero until Total is known
	Phase   string  // what the daemon is doing: "Downloading", "Extracting"
}

// SelfUpdate pulls the given dashboard image and spawns a short-lived
// "updater" container that recreates the dashboard service. The dashboard
// cannot recreate its own container (the command would die with it), so the
// updater runs independently and does the work described in updaterScript.
//
// The updater uses the freshly pulled image itself, which ships docker-cli
// and the compose plugin.
//
// report, if non-nil, is called as the pull advances — that transfer is the
// only part of an update long enough to be worth watching, and the only part
// this process is still alive for. It runs on the goroutine driving the pull,
// so it must not block.
//
// ctx must not be tied to an HTTP request: the pull below transfers the whole
// image and readily outlives a browser that gives up on the request.
func (c *Client) SelfUpdate(ctx context.Context, imageRef, socketNetwork string, report func(PullStatus)) error {
	// imageRef reaches a shell as part of updaterScript, and its tag comes
	// from a GitHub release name, which is attacker-chosen for anyone who can
	// point GITHUB_REPO elsewhere.
	if !safeImageRef(imageRef) {
		return fmt.Errorf("refusing to update to %q: unexpected characters in image reference", imageRef)
	}

	rc, err := c.api.ImagePull(ctx, imageRef, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("pull %s: %w", imageRef, err)
	}
	if err := drainPull(rc, report); err != nil {
		return fmt.Errorf("pull %s: %w", imageRef, err)
	}

	const updaterName = "quasar-updater"
	c.removeContainer(ctx, updaterName)

	created, err := c.api.ContainerCreate(ctx,
		&container.Config{
			Image:      imageRef,
			Entrypoint: []string{"/bin/sh", "-c"},
			Cmd:        []string{strings.ReplaceAll(updaterScript, "__IMAGE__", imageRef)},
			Env:        []string{"DOCKER_HOST=tcp://socket-proxy:2375"},
			Labels:     map[string]string{"quasar.updater": "true"},
		},
		&container.HostConfig{
			// Not auto-removed: the dashboard can't wait for this container
			// (it dies mid-recreation), so the exited container plus
			// storage/update.log are how a failed update is diagnosed after
			// the fact. The next SelfUpdate run removes the previous one.
			Binds: []string{"/opt/quasar:/opt/quasar"},
			// Covers the failures the script cannot handle itself, because
			// they kill the script: an OOM kill mid-recreate, or the daemon
			// going down while the dashboard has no container.
			RestartPolicy: container.RestartPolicy{
				Name:              container.RestartPolicyOnFailure,
				MaximumRetryCount: 2,
			},
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

// pullReportEvery caps how often a pull's progress is passed on. The daemon
// emits a line per chunk per layer — thousands of them — and the consumer is a
// browser polling once a second.
const pullReportEvery = 250 * time.Millisecond

// drainPull consumes an image pull's progress stream, reporting how far it has
// got to report and returning the failures the daemon only announces inside the
// stream. ImagePull returns a nil error as soon as the request is accepted, so
// an unauthorized or missing tag otherwise surfaces later as a baffling "No
// such image" from ContainerCreate.
func drainPull(rc io.ReadCloser, report func(PullStatus)) error {
	defer rc.Close()

	// Per layer, because the daemon interleaves them: every line is one layer's
	// running total, and only their sum says anything about the whole image.
	type layer struct{ current, total int64 }
	layers := map[string]*layer{}
	phase := ""
	last := time.Time{}

	emit := func(force bool) {
		if report == nil || (!force && time.Since(last) < pullReportEvery) {
			return
		}
		last = time.Now()
		var st PullStatus
		for _, l := range layers {
			st.Current += l.current
			st.Total += l.total
		}
		if st.Total > 0 {
			st.Percent = float64(st.Current) / float64(st.Total) * 100
		}
		st.Phase = phase
		report(st)
	}

	dec := json.NewDecoder(rc)
	for {
		var msg struct {
			ID             string `json:"id"`
			Status         string `json:"status"`
			Error          string `json:"error"`
			ProgressDetail struct {
				Current int64 `json:"current"`
				Total   int64 `json:"total"`
			} `json:"progressDetail"`
		}
		switch err := dec.Decode(&msg); {
		case err == io.EOF:
			emit(true)
			return nil
		case err != nil:
			return err
		case msg.Error != "":
			return fmt.Errorf("%s", msg.Error)
		}
		// Lines without an id are about the pull as a whole ("Pulling from
		// org/app", "Digest: sha256:…") and carry no layer to account for.
		if msg.ID == "" {
			continue
		}
		l := layers[msg.ID]
		if l == nil {
			l = &layer{}
			layers[msg.ID] = l
		}
		switch msg.Status {
		case "Downloading":
			// Extracting repeats the same byte counts a second time, so only
			// the transfer is counted or every layer would weigh double.
			l.current, l.total = msg.ProgressDetail.Current, msg.ProgressDetail.Total
			phase = "Downloading"
		case "Download complete", "Already exists":
			l.current = l.total
		case "Extracting":
			phase = "Extracting"
		}
		emit(false)
	}
}

// safeImageRef reports whether ref is limited to the characters a registry
// reference can legitimately contain, so it can be interpolated into a shell
// script without quoting concerns.
func safeImageRef(ref string) bool {
	if ref == "" {
		return false
	}
	for _, r := range ref {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("./-_:@", r):
		default:
			return false
		}
	}
	return true
}
