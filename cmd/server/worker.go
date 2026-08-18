package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"quasar/internal/station/worker"
)

// station-worker is the disposable process a station's script runs in.
//
// The dashboard re-executes this binary with that one argument, writes a call
// on its standard input and reads the answer from its standard output. The
// process is started for one call and dies at the end of it, holding no Docker
// socket, no database handle, no writable directory and no network — every
// capability is a request the parent checks and performs.
//
// It is a mode of the same binary rather than a second one so that the two can
// never be out of step: an install with a worker from one release and a
// dashboard from another is not a state anybody could get into.
const workerArg = "station-worker"

// runWorkerMode reports whether this process was started as a worker, and if
// so serves its one call. Called before anything in main opens a database or a
// Docker connection, because a worker must open neither.
func runWorkerMode() bool {
	if len(os.Args) < 2 || os.Args[1] != workerArg {
		return false
	}
	if err := worker.Serve(os.Stdin, os.Stdout, engine); err != nil {
		fmt.Fprintln(os.Stderr, "station worker:", err)
		os.Exit(1)
	}
	os.Exit(0)
	return true
}

// engine is where the JavaScript runtime is plugged in. The boundary comes
// first and on purpose: everything behind this function reaches the outside
// world only through the Requester it is handed, so there is no way to add a
// capability later that skips the parent's check.
func engine(call worker.Call, req worker.Requester) (json.RawMessage, error) {
	return nil, errors.New("this build has no script runtime yet")
}
