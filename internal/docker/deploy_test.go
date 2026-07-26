package docker

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"quasar/internal/db"
)

// A deploy starts the replacement container while the one it replaces is still
// running, so their names must differ or ContainerCreate would collide.
func TestDeployContainerNameIsUniquePerDeploy(t *testing.T) {
	first, second := deployContainerName("abc123"), deployContainerName("abc123")
	if first == second {
		t.Fatalf("two deploys produced the same container name: %s", first)
	}
	for _, name := range []string{first, second} {
		if !strings.HasPrefix(name, "qs-abc123-") {
			t.Errorf("name %q should extend the app's stable alias %q", name, ContainerName("abc123"))
		}
	}
}

// Old and new containers share one Traefik router while a deploy overlaps them,
// so Traefik's own health check is what keeps requests off the container that
// is not serving yet.
func TestTraefikLabelsCarryHealthcheckOnlyWhenConfigured(t *testing.T) {
	c := &Client{domain: "example.com"}
	const key = "traefik.http.services.qs-a1.loadbalancer.healthcheck.path"

	withPath := c.traefikLabels(&db.App{ID: "a1", Subdomain: "app", Port: 8080, HealthPath: "/healthz"})
	if withPath[key] != "/healthz" {
		t.Errorf("healthcheck path = %q, want /healthz", withPath[key])
	}

	withoutPath := c.traefikLabels(&db.App{ID: "a1", Subdomain: "app", Port: 8080})
	if _, ok := withoutPath[key]; ok {
		t.Error("an app with no health path should not get healthcheck labels")
	}
}

// The app label, not the container name, is how a container is traced back to
// its app now that names carry a per-deploy suffix.
func TestTraefikLabelsCarryAppLabel(t *testing.T) {
	c := &Client{domain: "example.com"}
	labels := c.traefikLabels(&db.App{ID: "a1", Subdomain: "app", Port: 8080})
	if labels[appLabel] != "a1" {
		t.Errorf("%s = %q, want a1", appLabel, labels[appLabel])
	}
}

func TestProbeOnce(t *testing.T) {
	tests := []struct {
		name   string
		status int
		wantOK bool
	}{
		{"serving", http.StatusOK, true},
		{"redirect", http.StatusMovedPermanently, true},
		// A wrong health path 404s; treating that as serving would retire a
		// working container in favour of one that may not be up.
		{"not found", http.StatusNotFound, false},
		{"server error", http.StatusInternalServerError, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()
			err := probeOnce(srv.URL)
			if tc.wantOK && err != nil {
				t.Errorf("status %d: unexpected error %v", tc.status, err)
			}
			if !tc.wantOK && err == nil {
				t.Errorf("status %d: expected an error", tc.status)
			}
		})
	}
}

func TestProbeOnceUnreachable(t *testing.T) {
	// Port 1 on localhost refuses connections, standing in for a container
	// that is up but not yet listening.
	if err := probeOnce("http://127.0.0.1:1/"); err == nil {
		t.Error("expected an error probing a closed port")
	}
}
