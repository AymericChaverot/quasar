// Package version exposes the build version, injected at compile time:
//
//	go build -ldflags "-X quasar/internal/version.Version=v1.2.3"
package version

var Version = "dev"

// TraefikImage is the edge router this release was built against: the same
// image docker-compose.yml pins, and the one the dashboard offers to update an
// install to.
//
// Deliberately the tested version rather than whatever is newest on Docker Hub.
// Traefik is the one component that, if it will not start, takes every site on
// the server down with it — so the version on offer is one that has been run
// against this release of Quasar, not one nobody has tried yet.
//
// Kept in step with docker-compose.yml by TestTraefikImageMatchesCompose, which
// fails if the two are ever bumped apart.
const TraefikImage = "traefik:v3.7.10"
