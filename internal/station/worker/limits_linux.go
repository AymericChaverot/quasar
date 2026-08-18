//go:build linux

package worker

import (
	"syscall"
	"time"
)

// selfLimit asks the kernel to hold the worker to its budget, as the first
// thing it does with the call.
//
// This is the second line and it is written like one. The parent samples the
// worker's resident size and kills it, which is what actually holds; these are
// here so that a worker the parent somehow stopped watching still cannot run
// away with the machine.
//
// Not RLIMIT_AS. The Go runtime reserves address space generously, and a
// ceiling low enough to be useful there kills the worker before it has run a
// line of script — which would turn every station action into a crash and
// teach whoever wrote it that stations do not work.
//
// RLIMIT_DATA is set well above the resident ceiling for the same reason: since
// Linux 4.7 it counts anonymous mappings, so the Go heap grows against it, and
// the number that matters is the one the parent enforces.
func selfLimit(call Call) {
	if call.MaxMemoryBytes > 0 {
		data := uint64(call.MaxMemoryBytes) * 4
		syscall.Setrlimit(syscall.RLIMIT_DATA, &syscall.Rlimit{Cur: data, Max: data})
	}
	if call.WallMS > 0 {
		// Seconds, rounded up, with one to spare: a script spending its whole
		// budget on the CPU should be stopped by the interrupt it can report,
		// not by a signal it cannot.
		secs := uint64((time.Duration(call.WallMS)*time.Millisecond + time.Second - 1) / time.Second)
		syscall.Setrlimit(syscall.RLIMIT_CPU, &syscall.Rlimit{Cur: secs + 1, Max: secs + 2})
	}
}
