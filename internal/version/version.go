// Package version exposes the build version, injected at compile time:
//
//	go build -ldflags "-X quasar/internal/version.Version=v1.2.3"
package version

var Version = "dev"
