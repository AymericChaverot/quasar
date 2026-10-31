package server

import (
	"net/url"
	"strings"
)

// The registries an image link can be drawn as. An image from any other one
// has no page Quasar can name, so it gets no link.
const (
	registryDockerHub = "dockerhub"
	registryGHCR      = "ghcr"
	registryQuay      = "quay"
)

// ImageLink is where an image reference can be read in a browser.
type ImageLink struct {
	// URL is the image's page on its registry.
	URL string
	// Label is the image's path without its registry, "nginx" or "owner/app".
	Label string
	// Tag is the tag the app was deployed with; empty for an untagged
	// reference or one pinned by digest.
	Tag string
	// Registry picks the mark drawn next to the link.
	Registry string
}

// imageLinkOf works out the registry page behind an image reference, or
// reports false when there is none: a registry Quasar has no page layout for,
// or a reference that does not parse.
//
// The registry is the first segment when it looks like a host — a dot, a
// port, or "localhost" — and Docker Hub otherwise, as the Engine reads it.
func imageLinkOf(ref string) (ImageLink, bool) {
	ref = strings.TrimSpace(ref)
	if i := strings.Index(ref, "@"); i >= 0 {
		ref = ref[:i]
	}
	if ref == "" {
		return ImageLink{}, false
	}
	registry, path := registryDockerHub, ref
	if first, rest, ok := strings.Cut(ref, "/"); ok &&
		(strings.ContainsAny(first, ".:") || first == "localhost") {
		path = rest
		registry = ""
		switch strings.ToLower(first) {
		case "docker.io", "index.docker.io", "registry-1.docker.io":
			registry = registryDockerHub
		case "ghcr.io":
			registry = registryGHCR
		case "quay.io":
			registry = registryQuay
		}
	}
	// The tag follows the last slash, so a registry port is not taken for one.
	tag := ""
	if i := strings.LastIndex(path, ":"); i > strings.LastIndex(path, "/") {
		path, tag = path[:i], path[i+1:]
	}
	path = strings.Trim(path, "/")
	if path == "" || registry == "" {
		return ImageLink{}, false
	}
	l := ImageLink{Label: path, Tag: tag, Registry: registry}
	switch registry {
	case registryDockerHub:
		// An official image lives under the library namespace, with a page
		// of its own.
		path = strings.TrimPrefix(path, "library/")
		l.Label = path
		if strings.Contains(path, "/") {
			l.URL = "https://hub.docker.com/r/" + path
		} else {
			l.URL = "https://hub.docker.com/_/" + path
		}
	case registryGHCR:
		owner, name, ok := strings.Cut(path, "/")
		if !ok {
			return ImageLink{}, false
		}
		// A package is listed under the repository of the same name, which
		// is how ghcr images are published in practice.
		last := name[strings.LastIndex(name, "/")+1:]
		l.URL = "https://github.com/" + url.PathEscape(owner) + "/" + url.PathEscape(last) +
			"/pkgs/container/" + url.PathEscape(name)
	case registryQuay:
		l.URL = "https://quay.io/repository/" + path
	}
	return l, true
}
