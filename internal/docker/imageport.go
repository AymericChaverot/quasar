package docker

// Routing a single-container app at the port it really listens on.
//
// An app's port is a number typed into the create form, which defaults to 80,
// and nothing ever checks it against the thing that gets deployed. That holds
// for as long as the image keeps serving on the port it served on the day the
// app was made — and stops the moment it does not. A repository that swaps a
// static site behind nginx for a server of its own moves from 80 to whatever
// that server listens on, and every file it changed says so: the Dockerfile
// EXPOSEs the new port, the image carries it. Only Quasar's stored number does
// not, so Traefik keeps offering the old one a valid certificate and a 502.
//
// So the deploy reads the port out of the image it is about to run and routes
// at that instead. The stored number stays the operator's — it is honoured
// whenever the image offers it at all, and it is what a deliberate choice of
// one exposed port over another is written in. It is only overruled when the
// image cannot serve it.

import (
	"context"
	"slices"
	"strconv"
	"strings"
)

// imageExposedPorts lists the TCP ports an image declares it listens on, in
// ascending order. It returns nothing for an image that declares none, which is
// not unusual: EXPOSE is documentation, and plenty of Dockerfiles omit it.
func (c *Client) imageExposedPorts(ctx context.Context, ref string) []int {
	info, err := c.api.ImageInspect(ctx, ref)
	if err != nil || info.Config == nil {
		return nil
	}
	var out []int
	for spec := range info.Config.ExposedPorts {
		// Keys are "4321/tcp" — or bare "4321" in images built by older
		// tooling, where tcp is the default the daemon assumes.
		port, proto, _ := strings.Cut(spec, "/")
		if proto != "" && proto != "tcp" {
			continue // Traefik routes HTTP, so a UDP port is never the answer
		}
		if n, err := strconv.Atoi(port); err == nil && n > 0 {
			out = append(out, n)
		}
	}
	slices.Sort(out)
	return out
}

// servedPort is the port Traefik is pointed at, given the port the app is
// configured with and the ports its image exposes. The second return value is
// why, ready to be written into the deploy log, and is empty when the
// configured port stands.
func servedPort(configured int, exposed []int) (int, string) {
	if len(exposed) == 0 || slices.Contains(exposed, configured) {
		// An image that exposes nothing has no opinion to follow, and one that
		// exposes the configured port agrees with it. Both leave it alone.
		return configured, ""
	}
	if len(exposed) == 1 {
		return exposed[0], "routing to port " + strconv.Itoa(exposed[0]) + ", the only port this image " +
			"exposes — the application is configured for " + strconv.Itoa(configured) +
			", which it does not"
	}
	// Several ports and no way to tell which serves the site: an image exposing
	// a web port beside a metrics one would as readily hand the domain to the
	// metrics port. The configured port is kept, wrong as it looks, because it
	// is the one thing here that someone actually chose.
	return configured, "this image exposes " + joinInts(exposed) + ", and not the port " +
		strconv.Itoa(configured) + " the application is configured for — if it answers on none of " +
		"them, set the right one in the application's settings"
}

// joinInts renders ports for the deploy log as "80, 3000 and 9090".
func joinInts(list []int) string {
	parts := make([]string, len(list))
	for i, n := range list {
		parts[i] = strconv.Itoa(n)
	}
	if len(parts) < 2 {
		return strings.Join(parts, "")
	}
	return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
}
