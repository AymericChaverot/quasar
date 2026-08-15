package catalog

import (
	"slices"
	"strconv"
	"strings"
)

// Param is a choice the operator makes when picking an entry — the version of a
// game server, the flavour of it, how much memory it gets — and it is what lets
// one entry serve a fleet. Somebody running several Minecraft servers wants a
// vanilla 1.20.1 beside a modded 1.21, and without parameters that is two
// near-identical entries to keep in step by hand, then five, then twenty.
//
// The value lands wherever the entry writes {{NAME}}: in its env, its image
// reference, its compose file, and in the name and subdomain proposed for the
// app. For a compose entry the usual shape is a one-line env declaration
// (NAME={{NAME}}) that the compose file then reads back as ${NAME}, because
// Quasar writes the env beside the compose file and passes it with --env-file.
type Param struct {
	// Name is the key: {{NAME}} in the entry, and by convention the env key too.
	Name string `yaml:"name"`

	// Label is what the form calls it; Name serves when this is empty.
	Label string `yaml:"label,omitempty"`

	// Kind is "text", "select", "number" or "port"; empty reads as "text".
	Kind string `yaml:"kind,omitempty"`

	Default string `yaml:"default,omitempty"`

	// Options are the only values accepted when Kind is "select".
	Options []string `yaml:"options,omitempty"`

	// Help is the line under the field, for the choice that is not obvious
	// from its name — which values a version string can take, say.
	Help string `yaml:"help,omitempty"`
}

// Type is the kind of field to draw, defaulting to free text.
func (p Param) Type() string {
	if p.Kind == "" {
		return "text"
	}
	return p.Kind
}

// Title is what the form labels the field.
func (p Param) Title() string {
	if p.Label != "" {
		return p.Label
	}
	return p.Name
}

// Accepts reports whether v is a value this parameter can take. A select is
// held to the values it offers, and everything else is held to a single line:
// a value carrying a newline would break out of the env line or the compose
// scalar it is substituted into.
func (p Param) Accepts(v string) bool {
	if strings.ContainsAny(v, "\n\r") {
		return false
	}
	switch p.Type() {
	case "select":
		return slices.Contains(p.Options, v)
	case "number":
		_, err := strconv.Atoi(v)
		return err == nil
	case "port":
		n, err := strconv.Atoi(v)
		return err == nil && n > 0 && n < 65536
	}
	return true
}

// Values are the parameter values picked for an entry, keyed by parameter name.
type Values map[string]string

// Resolve returns the values to render with: what was picked, where the entry
// offers that parameter and accepts the value, and the declared default
// everywhere else.
//
// Dropping what the entry does not declare is the point. These values arrive
// from a query string, and a hand-edited one must not be able to introduce a
// placeholder of its own or slip a value past a select's list.
func (t Template) Resolve(picked Values) Values {
	out := make(Values, len(t.Params))
	for _, p := range t.Params {
		v := strings.TrimSpace(picked[p.Name])
		if v == "" || !p.Accepts(v) {
			v = p.Default
		}
		out[p.Name] = v
	}
	return out
}

// substitute replaces every {{NAME}} in s with its value, in one pass over the
// string. One pass matters: a value that happens to contain {{SOMETHING}} is
// then left as the text it is rather than being resolved in turn.
func substitute(s string, v Values) string {
	if len(v) == 0 {
		return s
	}
	pairs := make([]string, 0, len(v)*2)
	for name, val := range v {
		pairs = append(pairs, "{{"+name+"}}", val)
	}
	return strings.NewReplacer(pairs...).Replace(s)
}

// slug reduces a filled-in subdomain to what the form accepts: lowercase
// letters, digits and hyphens. Version numbers are the reason it exists —
// "1.20.1" is an ordinary parameter value and not a legal DNS label, and an
// entry proposing "mc-{{VERSION}}" should not have to know that.
func slug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			if cur := b.String(); cur != "" && !strings.HasSuffix(cur, "-") {
				b.WriteByte('-')
			}
		}
	}
	return strings.Trim(b.String(), "-")
}
