package hostupdate

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// RebootRequired reports whether the host has installed something since it
// booted that only a restart puts into use. hostRoot is where the host's
// filesystem is mounted, kernel the release it is running (uname -r), and
// booted when it started.
//
// There is no one way to know. Debian and Ubuntu leave a flag file. Everywhere
// else it comes down to the kernel, the one update nothing short of a reboot
// applies: a newer kernel's modules appeared after the machine came up. That
// holds whether the old kernel is kept beside it (Fedora, RHEL, openSUSE…) or
// replaced in place (Arch).
//
// The running kernel's own modules being absent proves nothing: on a
// container VPS (LXC, OpenVZ) the kernel is the provider's and was never here.
func RebootRequired(hostRoot, kernel string, booted time.Time) bool {
	// /run itself rather than /var/run, which is an absolute symlink and would
	// be resolved inside this container.
	if _, err := os.Stat(filepath.Join(hostRoot, "run", "reboot-required")); err == nil {
		return true
	}
	if kernel == "" {
		return false
	}
	for _, dir := range []string{"usr/lib/modules", "lib/modules"} {
		entries, err := os.ReadDir(filepath.Join(hostRoot, dir))
		if err != nil || len(entries) == 0 {
			continue
		}
		for _, e := range entries {
			info, err := e.Info()
			if err == nil && e.IsDir() && info.ModTime().After(booted) && newerKernel(e.Name(), kernel) {
				return true
			}
		}
		return false
	}
	return false
}

// newerKernel compares two kernel releases by their numbers, in order —
// 6.11.4-301.fc41 is newer than 6.11.3-300.fc41, and 6.10 is older than 6.9
// only if compared as text. A modules directory from something that is not a
// kernel release at all has no numbers to compare and is never newer.
func newerKernel(a, b string) bool {
	na, nb := numbers(a), numbers(b)
	if len(na) == 0 {
		return false
	}
	for i := 0; i < len(na) && i < len(nb); i++ {
		if na[i] != nb[i] {
			return na[i] > nb[i]
		}
	}
	return len(na) > len(nb)
}

// numbers is every run of digits in s, in order.
func numbers(s string) []int {
	var out []int
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r < '0' || r > '9' }) {
		n, err := strconv.Atoi(f)
		if err == nil {
			out = append(out, n)
		}
	}
	return out
}
