package station

// Answering "may this station do this particular thing", for the parts of a
// permission that are narrowed by a name rather than by a flag.
//
// It lives here rather than beside the code that performs the capability
// because it is the half worth testing on its own: a glob that lets
// "data/mods/**" reach "data/../../etc/shadow" is a bug with no server, no
// worker and no database anywhere near it.

import (
	"path"
	"slices"
	"strings"
)

// AllowsPath reports whether the files permission covers rel.
//
// The path is cleaned first, so "data/mods/../../x" is judged as "x" and not as
// something under data/mods. That is only the first of the two defences: the
// second is that every path is resolved through its symlinks before it is
// touched, because a link dropped into an application's own volume is
// otherwise a way out on the first write.
func (p Permissions) AllowsPath(rel string) bool {
	clean := cleanRel(rel)
	if clean == "" {
		return false
	}
	return slices.ContainsFunc(p.Files.Paths, func(pattern string) bool {
		return matchGlob(cleanRel(pattern), clean)
	})
}

// AllowsEnvRead and AllowsEnvWrite are per key rather than wholesale: a
// station has no business reading a database password it did not generate.
func (p Permissions) AllowsEnvRead(key string) bool {
	return slices.Contains(p.Env.Read, key) || slices.Contains(p.Env.Write, key)
}

func (p Permissions) AllowsEnvWrite(key string) bool { return slices.Contains(p.Env.Write, key) }

// AllowsHost reports whether net.external names this host. Exactly: there are
// no wildcards, so this is a list membership and not a pattern match, which is
// the whole reason the list is worth showing to an operator.
func (p Permissions) AllowsHost(host string) bool {
	return slices.Contains(p.NetExternal.Allow, strings.ToLower(host))
}

// AllowsInternal reports whether net.internal names this service and this
// port. Both, not either: a permission to reach a service on 8123 is not a
// permission to reach its admin port.
func (p Permissions) AllowsInternal(service string, port int) bool {
	return slices.Contains(p.NetInternal.Services, service) && slices.Contains(p.NetInternal.Ports, port)
}

// cleanRel normalises a path the way the confinement does: relative to the
// application's own folder, with any attempt to climb above it collapsed
// rather than trimmed. A path that resolves to the folder itself, or above it,
// comes back empty and matches nothing.
func cleanRel(rel string) string {
	rel = strings.ReplaceAll(rel, `\`, "/")
	clean := path.Clean("/" + strings.TrimPrefix(rel, "/"))
	return strings.TrimPrefix(clean, "/")
}

// matchGlob matches a cleaned path against a cleaned pattern, segment by
// segment, with ** standing for any number of segments including none.
//
// path.Match is not enough on its own: its * does not cross a separator, which
// is right, and it has no way to say "everything under here", which is what
// every station writing a files permission wants to say.
func matchGlob(pattern, name string) bool {
	return matchSegments(strings.Split(pattern, "/"), strings.Split(name, "/"))
}

func matchSegments(pattern, name []string) bool {
	for len(pattern) > 0 {
		if pattern[0] == "**" {
			// Zero segments, or one more consumed and asked again. The
			// recursion is bounded by the length of the path, which is bounded
			// by the filesystem.
			if matchSegments(pattern[1:], name) {
				return true
			}
			if len(name) == 0 {
				return false
			}
			name = name[1:]
			continue
		}
		if len(name) == 0 {
			return false
		}
		if ok, err := path.Match(pattern[0], name[0]); err != nil || !ok {
			return false
		}
		pattern, name = pattern[1:], name[1:]
	}
	return len(name) == 0
}
