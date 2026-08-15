package docker

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/pkg/stdcopy"
)

// traefikOverrideMark is the first line of the file the pin is written to, and
// the proof that Quasar wrote it. An operator's own override file is left
// alone rather than overwritten — it is theirs, and it may be the only place
// something about this install is recorded.
const traefikOverrideMark = "# Written by Quasar: the Traefik version this dashboard was tested with."

// traefikUpdaterScript recreates the edge router on a new image, and guarantees
// that a Traefik container is running by the time it exits — rolling back to
// the previous image if the new one will not stay up. __IMAGE__ and __MARK__
// are substituted before the script is handed to the updater container.
//
// The pin goes in docker-compose.override.yml rather than into
// docker-compose.yml itself. /opt/quasar is a git clone: editing the tracked
// compose file would leave the working tree dirty, and the next `git pull
// --ff-only` — which is how setup.sh updates an install — would refuse to run.
// An override file is untracked, compose merges it automatically, and it
// applies to an operator's own `docker compose up -d` as much as to this
// script, so the version cannot silently revert the next time the stack is
// brought up by hand.
//
// --no-deps matters as much here as in the self-updater: without it compose
// also evaluates traefik's depends_on and may recreate socket-proxy, which is
// the connection this very script is talking to the daemon through.
const traefikUpdaterScript = `
set -u

# Everything is said twice: to this container's log, which the dashboard reads
# back to report what happened, and to storage/update.log, which is on a
# mounted volume and outlives the container. The dashboard's own updater can
# only use the file — it is being replaced as it writes — so that is where an
# operator already looks after an update.
LOG=/opt/quasar/storage/update.log
say() { echo "$*" | tee -a "$LOG"; }

say "=== $(date -u '+%Y-%m-%dT%H:%M:%SZ') traefik update to __IMAGE__ ==="

# Exit 1, not 2: nothing has been touched, so Traefik is still running.
cd /opt/quasar || { say "/opt/quasar not mounted"; exit 1; }

OVERRIDE=docker-compose.override.yml
MARK='__MARK__'

if [ -e "$OVERRIDE" ] && ! head -n 1 "$OVERRIDE" | grep -qF "$MARK"; then
	say "$OVERRIDE exists and Quasar did not write it; refusing to overwrite it"
	exit 1
fi

# Empty means no pin yet, and rolling back means removing the file so the
# image in docker-compose.yml takes over again.
PREV=""
[ -e "$OVERRIDE" ] && PREV=$(sed -n 's/^ *image: *//p' "$OVERRIDE" | tail -n 1)

pin() {
	cat > "$OVERRIDE" <<EOF
$MARK
# Remove this file to fall back to the image pinned in docker-compose.yml.
services:
  traefik:
    image: $1
EOF
}

restore() {
	if [ -n "$PREV" ]; then pin "$PREV"; else rm -f "$OVERRIDE"; fi
}

# The exit code is ignored on purpose: compose reports the request it made, and
# what matters is whether the router is still up a moment later, which stable()
# is what answers.
up() { docker compose up -d --no-build --no-deps traefik 2>&1 | tee -a "$LOG"; }

# Running now and still running five seconds later, so a router that boots and
# immediately crash-loops is not mistaken for a successful update.
stable() {
	docker inspect -f '{{.State.Running}}' quasar-traefik 2>/dev/null | grep -qx true || return 1
	sleep 5
	docker inspect -f '{{.State.Running}}' quasar-traefik 2>/dev/null | grep -qx true
}

pin __IMAGE__
n=0
while [ "$n" -lt 3 ]; do
	up
	if stable; then
		say "traefik running on __IMAGE__"
		exit 0
	fi
	n=$((n + 1))
	say "attempt $n did not bring traefik up"
	sleep 3
done

say "__IMAGE__ will not stay up, rolling back to ${PREV:-the compose default}"
restore
up
if stable; then
	say "rolled back to ${PREV:-the compose default}"
	# Non-zero even though the router is up: the update did not happen, and the
	# dashboard is alive to say so.
	exit 1
fi

say "rollback failed, traefik is DOWN"
exit 2
`

// traefikUpdaterName is the container the script above runs in. Fixed, so a
// previous run's container is what the next one removes.
const traefikUpdaterName = "quasar-traefik-updater"

// TraefikProgress is how far an update has got. The two phases are worth
// telling apart on screen: during the pull nothing is at risk and the sites are
// up, and from the recreate onwards the edge router is being replaced.
type TraefikProgress struct {
	Pull       PullStatus
	Recreating bool
}

// UpdateTraefik pulls imageRef and recreates the edge router on it, returning
// only once that has either worked or been rolled back.
//
// Unlike the dashboard's self-update, this one can be watched: nothing here
// replaces the container this process runs in, so the work is handed to a
// short-lived container and then waited on, and what the operator is told is
// the actual outcome rather than the fact that an attempt was started.
//
// report, if non-nil, is called as the work advances. It runs on the goroutine
// driving the update, so it must not block.
//
// ctx must not be tied to an HTTP request: recreating Traefik takes down the
// connection the request arrived over.
func (c *Client) UpdateTraefik(ctx context.Context, imageRef string, report func(TraefikProgress)) error {
	// imageRef reaches a shell inside the script below.
	if !safeImageRef(imageRef) {
		return fmt.Errorf("refusing to update to %q: unexpected characters in image reference", imageRef)
	}

	rc, err := c.api.ImagePull(ctx, imageRef, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("pull %s: %w", imageRef, err)
	}
	var pullReport func(PullStatus)
	if report != nil {
		pullReport = func(p PullStatus) { report(TraefikProgress{Pull: p}) }
	}
	if err := drainPull(rc, pullReport); err != nil {
		return fmt.Errorf("pull %s: %w", imageRef, err)
	}
	// Said before it happens rather than after: from here on the router is
	// being replaced, and the page asking for this update is going with it.
	if report != nil {
		report(TraefikProgress{Recreating: true})
	}

	// The updater runs from the dashboard's own image, which is the one image
	// on this host known to carry the docker CLI and the compose plugin.
	self, err := c.api.ContainerInspect(ctx, "quasar-dashboard")
	if err != nil {
		return fmt.Errorf("find the dashboard's own image: %w", err)
	}

	c.removeContainer(ctx, traefikUpdaterName)
	script := strings.NewReplacer("__IMAGE__", imageRef, "__MARK__", traefikOverrideMark).Replace(traefikUpdaterScript)
	created, err := c.api.ContainerCreate(ctx,
		&container.Config{
			Image:      self.Config.Image,
			Entrypoint: []string{"/bin/sh", "-c"},
			Cmd:        []string{script},
			Env:        []string{"DOCKER_HOST=tcp://socket-proxy:2375"},
			Labels:     map[string]string{"quasar.updater": "traefik"},
		},
		&container.HostConfig{
			Binds: []string{"/opt/quasar:/opt/quasar"},
			// No restart policy, unlike the self-updater: that one retries
			// because a dashboard left with no container cannot ask for help.
			// Here the script has already put Traefik back, and repeating an
			// update that does not work would only take the edge router down
			// again every few seconds.
		},
		&network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{c.socketNet: {}},
		},
		nil, traefikUpdaterName)
	if err != nil {
		return fmt.Errorf("create traefik updater: %w", err)
	}
	if err := c.api.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		return fmt.Errorf("start traefik updater: %w", err)
	}

	statusCh, errCh := c.api.ContainerWait(ctx, created.ID, container.WaitConditionNotRunning)
	select {
	case err := <-errCh:
		return fmt.Errorf("watch traefik updater: %w", err)
	case st := <-statusCh:
		if st.StatusCode == 0 {
			return nil
		}
		return fmt.Errorf("%s", traefikFailure(st.StatusCode, c.lastLines(ctx, created.ID, 4)))
	}
}

// traefikFailure turns the updater's exit code into the sentence the operator
// reads. The two codes mean very different things — one is an update that did
// not take, the other is a router that is down — and the difference decides
// whether anything needs doing right now.
func traefikFailure(code int64, detail string) string {
	msg := "the update was rolled back and Traefik is running on its previous version"
	if code != 1 {
		msg = "Traefik is DOWN — run `docker compose up -d traefik` on the server"
	}
	if detail != "" {
		msg += ": " + detail
	}
	return msg
}

// lastLines reads the tail of a finished container's output, for an error
// message that says what actually went wrong rather than only that something
// did.
func (c *Client) lastLines(ctx context.Context, id string, n int) string {
	rc, err := c.api.ContainerLogs(ctx, id, container.LogsOptions{
		ShowStdout: true, ShowStderr: true, Tail: fmt.Sprint(n),
	})
	if err != nil {
		return ""
	}
	defer rc.Close()
	var buf bytes.Buffer
	if _, err := stdcopy.StdCopy(&buf, &buf, io.LimitReader(rc, 64<<10)); err != nil {
		return ""
	}
	return strings.Join(strings.Fields(strings.TrimSpace(buf.String())), " ")
}
