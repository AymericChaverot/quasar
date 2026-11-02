package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"quasar/internal/statuspage"
)

// status-page is the public status page, run in a container of its own by the
// dashboard when the operator deploys one.
//
// A mode of this binary rather than a program of its own, for the reason the
// worker is: one image, so the page and the API it reads are always from the
// same release. It opens no database and no Docker connection; it reads the
// status API at QUASAR_STATUS_API with the key in QUASAR_STATUS_KEY and
// serves the page on :8080.
const statusPageArg = "status-page"

// runStatusPageMode serves the page and exits, if this process was started
// as one.
func runStatusPageMode() {
	if len(os.Args) < 2 || os.Args[1] != statusPageArg {
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	addr := os.Getenv("QUASAR_STATUS_LISTEN")
	if addr == "" {
		addr = ":8080"
	}
	err := statuspage.ListenAndServe(ctx, addr, statuspage.Config{
		API: os.Getenv("QUASAR_STATUS_API"),
		Key: os.Getenv("QUASAR_STATUS_KEY"),
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "status page:", err)
		os.Exit(1)
	}
	os.Exit(0)
}
