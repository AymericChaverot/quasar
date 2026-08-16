//go:build linux

package files

import "syscall"

// wOK is access(2)'s W_OK. The syscall package does not export it on Linux, and
// pulling in x/sys for one integer would be a dependency for a constant.
const wOK = 0x2

// canWrite reports whether this process may create and remove entries in dir.
//
// access(2) is the right question to ask here, and the only one that answers it
// without touching the directory. The dashboard runs as root in its container,
// so permission bits alone always look permissive — but a read-only bind mount
// returns EROFS to root as readily as to anyone else, and read-only bind mounts
// are exactly what stands between this and a named volume on most installs.
//
// Asking rather than assuming is what lets the same build serve an install
// where Docker's volume tree is mounted in read-write and one where it is not,
// with the interface offering an edit in the first and not in the second.
func canWrite(dir string) bool {
	return syscall.Access(dir, wOK) == nil
}
