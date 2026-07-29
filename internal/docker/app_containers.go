package docker

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"

	"quasar/internal/db"
)

// AppContainer is one container of an app's compose project.
//
// A stack deployed as a single status line is opaque: "error" can mean the
// database never came up, or that one worker exited while everything serving
// traffic is fine. Listing the project's containers is what makes the
// difference visible, and gives each one somewhere to show its own logs.
type AppContainer struct {
	Name    string
	Service string // the compose service it was created from
	Image   string
	State   string // "running", "exited", ...
	Uptime  string // human duration, empty unless running
	IsWeb   bool   // carries the Traefik labels, i.e. this is what serves the app
}

// AppContainers lists the containers of an app's compose project, by service
// name. It returns nil for an app that is not a stack — a single-container app
// has nothing to break down.
func (c *Client) AppContainers(ctx context.Context, a *db.App) []AppContainer {
	if !c.UsesCompose(a) {
		return nil
	}
	list := c.composeContainers(ctx, a.ID)
	out := make([]AppContainer, 0, len(list))
	for _, ct := range list {
		out = append(out, appContainerFrom(ct))
	}
	sortAppContainers(out)
	return out
}

// sortAppContainers orders a stack by service name rather than by the creation
// order the daemon returns: the list is polled, and containers recreated by a
// deploy would otherwise reshuffle under the reader between refreshes.
func sortAppContainers(out []AppContainer) {
	sort.Slice(out, func(i, j int) bool {
		if out[i].Service != out[j].Service {
			return out[i].Service < out[j].Service
		}
		return out[i].Name < out[j].Name
	})
}

func appContainerFrom(ct container.Summary) AppContainer {
	name := ""
	if len(ct.Names) > 0 {
		name = strings.TrimPrefix(ct.Names[0], "/")
	}
	ac := AppContainer{
		Name:    name,
		Service: ct.Labels["com.docker.compose.service"],
		Image:   ct.Image,
		State:   ct.State,
		IsWeb:   ct.Labels["traefik.enable"] == "true",
	}
	if ct.State == "running" {
		ac.Uptime = humanDuration(time.Since(time.Unix(ct.Created, 0)))
	}
	return ac
}

// GetAppContainer resolves one container of an app's compose project by name.
//
// The lookup deliberately goes through the project's own container list rather
// than inspecting the name directly: that is what stops a URL naming another
// app's container — or quasar-dashboard — from returning its logs to whoever
// can read this app.
func (c *Client) GetAppContainer(ctx context.Context, a *db.App, name string) (AppContainer, error) {
	for _, ac := range c.AppContainers(ctx, a) {
		if ac.Name == name {
			return ac, nil
		}
	}
	return AppContainer{}, ErrNoContainer
}
