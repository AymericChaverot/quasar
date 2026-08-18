//go:build !linux

package worker

// selfLimit does nothing where there is no setrlimit to call.
//
// The platforms this build serves are development machines; Quasar runs on
// Linux. What matters is that the difference stops here: the process boundary
// and the parent's sample-and-kill watchdog are the same on both, so a station
// is isolated and bounded where this is written as well as where it is
// deployed, and only the second line of defence is missing.
func selfLimit(call Call) {}
