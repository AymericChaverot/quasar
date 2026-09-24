package main

import (
	"fmt"
	"os"

	"quasar/internal/banner"
	"quasar/internal/version"
)

// printBanner opens the dashboard's log with its banner and the version that
// is starting. Straight to stderr, where log writes, but without log's
// timestamp, which would cut through the art.
//
// In colour even though Docker's log is not a terminal: that log is where the
// banner is read, in `docker logs` and on the container's page. NO_COLOR
// (no-color.org) asks for the one-line form instead.
func printBanner() {
	if os.Getenv("NO_COLOR") != "" {
		fmt.Fprint(os.Stderr, banner.Plain(version.Version))
		return
	}
	fmt.Fprint(os.Stderr, "\n"+banner.Render(version.Version)+"\n")
}
