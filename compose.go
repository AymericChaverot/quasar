// Package quasar holds what the repository ships beside the code and the
// binary needs to know about.
package quasar

import _ "embed"

// ComposeFile is the docker-compose.yml this version was released with: the
// system stack — Traefik, the socket proxy and the dashboard — as it expects
// to be run.
//
// An install is a clone of the repository, and the dashboard's own update only
// replaces its image; the compose file on the server stays whatever it was
// when the install last pulled. Carrying the file in the binary is what lets
// the dashboard tell what the server is missing.
//
//go:embed docker-compose.yml
var ComposeFile []byte
