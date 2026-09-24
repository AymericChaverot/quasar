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

// TraefikConfig is traefik/traefik.yml as this version shipped it, with the
// {{ACME_EMAIL}} placeholder Traefik fills in as it starts. Compared with the
// file on the server, it tells an install whose file still carries the email
// setup.sh used to write into it.
//
//go:embed traefik/traefik.yml
var TraefikConfig []byte
