package docker

// Reading a compose file the way compose will read it.
//
// A compose file is a template: `"${HTTP_PORT:-8080}:80"` is not a port until
// the variables in it are substituted. Quasar reads such a file twice, and used
// to read it two different ways — the rewrite in compose_adapt.go parsed the
// YAML as written, while the conflict check in compose_ports.go asked compose
// itself, which substitutes. A file parameterising its host port therefore came
// out of the rewrite with the binding still in it, and out of the check as a
// collision with Traefik, reported with a remedy telling the operator to do by
// hand what Quasar believed it had already done.
//
// So the rewrite substitutes too, from the same environment compose is given.

import "strings"

// envMap turns .env-style content into the variables compose interpolates the
// file with. It is the same content that reaches compose as --env-file, so both
// resolve a reference the same way.
func envMap(content string) map[string]string {
	out := map[string]string{}
	for _, line := range envLines(content) {
		if key, value, ok := strings.Cut(line, "="); ok {
			// Compose strips one layer of quotes around a value in an env file.
			out[strings.TrimSpace(key)] = trimQuotes(value)
		}
	}
	return out
}

func trimQuotes(v string) string {
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
		return v[1 : len(v)-1]
	}
	return v
}

// resolveVars substitutes compose's variable references in a value, so what
// this package reads is what compose will act on.
//
// An undefined variable with no default becomes empty, which is what compose
// does — it warns and carries on. Getting that wrong in the other direction
// would be worse than leaving the reference alone: a port entry read as binding
// something it does not is a binding stripped out of a stack that wanted it.
func resolveVars(s string, env map[string]string) string {
	if !strings.ContainsRune(s, '$') {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] != '$' {
			b.WriteByte(s[i])
			i++
			continue
		}
		// "$$" is how a compose file writes a literal dollar sign.
		if i+1 < len(s) && s[i+1] == '$' {
			b.WriteByte('$')
			i += 2
			continue
		}
		if i+1 < len(s) && s[i+1] == '{' {
			end := strings.IndexByte(s[i+2:], '}')
			if end < 0 {
				b.WriteString(s[i:]) // unterminated, so not a reference at all
				break
			}
			b.WriteString(lookup(s[i+2:i+2+end], env))
			i += 2 + end + 1
			continue
		}
		name, width := bareName(s[i+1:])
		if width == 0 {
			b.WriteByte('$') // a lone "$", which compose leaves as it is
			i++
			continue
		}
		b.WriteString(env[name])
		i += 1 + width
	}
	return b.String()
}

// lookup resolves the inside of a "${...}", in the four forms compose accepts:
// a bare name, ":-"/"-" with a default, and ":?"/"?" with an error message.
//
// The two error forms have no value of their own to fall back on: compose stops
// on them, so nothing this returns for one will ever be deployed.
func lookup(expr string, env map[string]string) string {
	for _, sep := range []string{":-", "-", ":?", "?"} {
		name, alt, ok := strings.Cut(expr, sep)
		if !ok {
			continue
		}
		value, set := env[name]
		// ":-" also falls back when the variable is set but empty; "-" only
		// when it is unset.
		if set && (value != "" || sep == "-" || sep == "?") {
			return value
		}
		if sep == ":-" || sep == "-" {
			return alt
		}
		return ""
	}
	return env[expr]
}

// bareName reads the variable name of an unbraced "$NAME" reference and how
// many bytes it spans, or 0 when what follows the "$" cannot start a name.
func bareName(s string) (string, int) {
	n := 0
	for n < len(s) && (s[n] == '_' ||
		(s[n] >= 'a' && s[n] <= 'z') || (s[n] >= 'A' && s[n] <= 'Z') ||
		(n > 0 && s[n] >= '0' && s[n] <= '9')) {
		n++
	}
	return s[:n], n
}
