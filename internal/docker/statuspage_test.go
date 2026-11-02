package docker

import "testing"

// The page is routed on its own host, over HTTPS, through the network Traefik
// reads, with the headers a public page carries.
func TestTheStatusPageIsRoutedOnItsHost(t *testing.T) {
	l := statusPageLabels("status.example.com", "traefik-net")
	for k, want := range map[string]string{
		"traefik.enable":                                                   "true",
		"traefik.docker.network":                                           "traefik-net",
		"traefik.http.routers.quasar-status.rule":                          "Host(`status.example.com`)",
		"traefik.http.routers.quasar-status.entrypoints":                   "websecure",
		"traefik.http.routers.quasar-status.tls.certresolver":              "letsencrypt",
		"traefik.http.routers.quasar-status.middlewares":                   "quasar-status-headers",
		"traefik.http.services.quasar-status.loadbalancer.server.port":     "8080",
		"traefik.http.middlewares.quasar-status-headers.headers.frameDeny": "true",
	} {
		if l[k] != want {
			t.Errorf("%s = %q, want %q", k, l[k], want)
		}
	}
	if _, isApp := l[appLabel]; isApp {
		t.Error("the page is labelled as an application, so it would be listed and managed as one")
	}
}
