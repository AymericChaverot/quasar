//go:build !linux

package files

import "os"

// canWrite reports whether this process may create entries in dir, by trying.
//
// The platforms this build serves are development machines: Quasar itself runs
// on Linux, where the access(2) version answers without writing anything. Here
// there is no portable equivalent — Windows reports a directory as writable
// from its permission bits while the create still fails — so the honest test is
// the operation itself, undone immediately.
func canWrite(dir string) bool {
	f, err := os.CreateTemp(dir, ".quasar-probe-*")
	if err != nil {
		return false
	}
	// The create already answered the question. Tidying up after it is a
	// courtesy, and a probe file left behind is not worth reporting as a
	// directory nobody can write to.
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return true
}
