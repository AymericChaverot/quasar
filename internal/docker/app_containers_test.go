package docker

import (
	"context"
	"testing"

	"github.com/docker/docker/api/types/container"

	"quasar/internal/db"
)

func summary(name, service, state string, labels map[string]string) container.Summary {
	all := map[string]string{"com.docker.compose.service": service}
	for k, v := range labels {
		all[k] = v
	}
	return container.Summary{Names: []string{"/" + name}, State: state, Image: service + ":latest", Labels: all}
}

// The list is polled every few seconds. Left in the creation order the daemon
// returns, a redeployed stack would reshuffle under the reader's cursor.
func TestSortAppContainersByService(t *testing.T) {
	list := []AppContainer{
		{Service: "nginx", Name: "qs-a1-nginx-1"},
		{Service: "backend", Name: "qs-a1-backend-1"},
		{Service: "frontend", Name: "qs-a1-frontend-1"},
		{Service: "backend", Name: "qs-a1-backend-2"}, // a scaled service
	}
	sortAppContainers(list)

	want := []string{"qs-a1-backend-1", "qs-a1-backend-2", "qs-a1-frontend-1", "qs-a1-nginx-1"}
	for i, name := range want {
		if list[i].Name != name {
			t.Errorf("position %d = %q, want %q", i, list[i].Name, name)
		}
	}
}

// The fields the list and the detail page render, read off what the daemon
// actually returns.
func TestAppContainerFromSummary(t *testing.T) {
	ac := appContainerFrom(summary("qs-a1-nginx-1", "nginx", "running",
		map[string]string{"traefik.enable": "true"}))

	if ac.Name != "qs-a1-nginx-1" {
		t.Errorf("Name = %q, want the leading slash stripped", ac.Name)
	}
	if ac.Service != "nginx" {
		t.Errorf("Service = %q, want nginx", ac.Service)
	}
	if !ac.IsWeb {
		t.Error("a container carrying traefik.enable=true is the routed one")
	}
	if ac.Uptime == "" {
		t.Error("a running container must report an uptime")
	}
}

// A stopped container has no uptime to report, and printing one would claim it
// is up.
func TestAppContainerStoppedHasNoUptime(t *testing.T) {
	ac := appContainerFrom(summary("qs-a1-worker-1", "worker", "exited", nil))
	if ac.Uptime != "" {
		t.Errorf("Uptime = %q for an exited container, want empty", ac.Uptime)
	}
	if ac.IsWeb {
		t.Error("a container with no Traefik label is not the routed one")
	}
}

// A single-container app has no project to break down. It must not reach the
// compose path at all — the section is hidden for it, and GetAppContainer is
// what a crafted URL would otherwise go through.
func TestAppContainersEmptyForNonStacks(t *testing.T) {
	c := &Client{appsDir: t.TempDir()}
	ctx := context.Background()

	// A git app with a Dockerfile checkout: builds one image, runs one
	// container, and is not a stack.
	git := &db.App{ID: "a1", DeployType: "git"}
	checkout(t, c, git.ID, "Dockerfile")

	for _, a := range []*db.App{{ID: "a2", DeployType: "image", ImageRef: "nginx"}, git} {
		if got := c.AppContainers(ctx, a); got != nil {
			t.Errorf("AppContainers(%s app) = %v, want nil", a.DeployType, got)
		}
		if _, err := c.GetAppContainer(ctx, a, "quasar-dashboard"); err == nil {
			t.Errorf("GetAppContainer(%s app) resolved a container outside any project", a.DeployType)
		}
	}
}
