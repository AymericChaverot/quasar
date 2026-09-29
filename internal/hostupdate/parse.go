package hostupdate

import (
	"strings"
)

// Every parser reads a list meant for a person, so each one keeps only the
// lines shaped like a package and passes over headers, notices and blank
// lines, whatever the version of the tool printed around them.

// parseApt reads `apt list --upgradable`:
//
//	curl/noble-updates 8.5.0-2ubuntu10.6 amd64 [upgradable from: 8.5.0-2ubuntu10.5]
func parseApt(raw string) []Package {
	var out []Package
	for line := range strings.SplitSeq(raw, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || !strings.Contains(f[0], "/") {
			continue
		}
		name, _, _ := strings.Cut(f[0], "/")
		out = append(out, Package{Name: name, Version: f[1]})
	}
	return out
}

// parseRPM reads `dnf check-update` and `yum check-update`:
//
//	kernel.x86_64    6.11.4-301.fc41    updates
//
// A section of obsoleted packages may follow, and what is in it repeats what
// is above it.
func parseRPM(raw string) []Package {
	var out []Package
	for line := range strings.SplitSeq(raw, "\n") {
		if strings.HasPrefix(line, "Obsoleting") {
			break
		}
		f := strings.Fields(line)
		if len(f) != 3 || !strings.ContainsAny(f[1], "0123456789") {
			continue
		}
		dot := strings.LastIndex(f[0], ".")
		if dot <= 0 {
			continue
		}
		out = append(out, Package{Name: f[0][:dot], Version: f[1]})
	}
	return out
}

// parseZypper reads the table `zypper list-updates` draws:
//
//	S | Repository | Name | Current Version | Available Version | Arch
//	--+------------+------+-----------------+-------------------+-------
//	v | Main       | curl | 8.6.0-1.1       | 8.7.1-1.1         | x86_64
func parseZypper(raw string) []Package {
	var out []Package
	for line := range strings.SplitSeq(raw, "\n") {
		cols := strings.Split(line, "|")
		if len(cols) < 6 || strings.TrimSpace(cols[0]) != "v" {
			continue
		}
		out = append(out, Package{Name: strings.TrimSpace(cols[2]), Version: strings.TrimSpace(cols[4])})
	}
	return out
}

// parsePacman reads `checkupdates` and `pacman -Qu`:
//
//	linux 6.11.3.arch1-1 -> 6.11.4.arch1-1
func parsePacman(raw string) []Package {
	var out []Package
	for line := range strings.SplitSeq(raw, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 || f[2] != "->" {
			continue
		}
		out = append(out, Package{Name: f[0], Version: f[3]})
	}
	return out
}

// parseApk reads `apk version -l '<'`, whose first column glues the name to
// the installed version:
//
//	Installed:                                Available:
//	busybox-1.36.1-r29                      < 1.36.1-r30
func parseApk(raw string) []Package {
	var out []Package
	for line := range strings.SplitSeq(raw, "\n") {
		f := strings.Fields(line)
		if len(f) < 3 || f[1] != "<" {
			continue
		}
		out = append(out, Package{Name: apkName(f[0]), Version: f[2]})
	}
	return out
}

// apkName strips the version from "name-1.2.3-r4". Names carry hyphens too,
// so it is the last two segments that go, not everything after the first.
func apkName(s string) string {
	for range 2 {
		if i := strings.LastIndex(s, "-"); i > 0 {
			s = s[:i]
		}
	}
	return s
}
