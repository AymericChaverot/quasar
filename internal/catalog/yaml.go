package catalog

// This file is the operator's half of the catalogue: the YAML document a
// Quasar install can be given so the page offers the software that install
// actually runs, filed under the categories its operator thinks in.
//
// The format is the Template struct's yaml tags and nothing else — one entry
// shape, whether it was compiled in or written by hand. A document looks like:
//
//	name: My servers
//	categories: [Minecraft, Other games]
//	entries:
//	  - id: minecraft
//	    name: Minecraft server
//	    category: Minecraft
//	    deploy_type: compose
//	    subdomain: "mc-{{VERSION}}"
//	    params:
//	      - {name: VERSION, label: Version, default: "1.20.1"}
//	    env: |
//	      VERSION={{VERSION}}
//	    compose: |
//	      ...
//
// Two levels of interpolation meet in an entry and the difference is worth
// holding on to: {{NAME}} is resolved by Quasar when the entry is picked, and
// ${NAME} is left alone for docker compose to read from the .env at run time.

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// document is the file as written: a catalogue, with its entries under a key of
// their own rather than at the top level, so the categories have somewhere to
// live and the whole thing has a name.
type document struct {
	Name       string     `yaml:"name"`
	Categories []string   `yaml:"categories,omitempty"`
	Entries    []Template `yaml:"entries"`
}

// Parse reads an operator's catalogue. The name given on the document wins over
// the one passed in, which is only a fallback for a document that omits it.
//
// KnownFields is on: a misspelled key is the likeliest mistake in a hand-written
// entry, and it is one YAML would otherwise swallow in silence — "deploytype"
// would parse cleanly and deploy the wrong kind of app.
func Parse(name, doc string) (Catalog, error) {
	var d document
	dec := yaml.NewDecoder(strings.NewReader(doc))
	dec.KnownFields(true)
	if err := dec.Decode(&d); err != nil {
		return Catalog{}, fmt.Errorf("this is not a catalogue Quasar can read: %s", readable(err))
	}
	if d.Name != "" {
		name = d.Name
	}
	c := Catalog{Name: name, Categories: d.Categories, Templates: d.Entries}
	for i := range c.Templates {
		c.Templates[i].Source = name
	}
	return c, nil
}

// unknownKeyRe matches the decoder's way of reporting a key it does not know.
var unknownKeyRe = regexp.MustCompile(`field (\S+) not found`)

// goTypeNames turns the Go types the decoder names into the parts of a document
// an operator wrote.
var goTypeNames = strings.NewReplacer(
	" in type catalog.Template", " on an entry",
	" in type catalog.Param", " on a parameter",
	" in type catalog.document", " at the top level",
)

// readable rewrites the decoder's message into something worth showing.
// yaml.v3 reports a misspelled key as `field deploytype not found in type
// catalog.Template` — accurate, and no help at all to somebody writing a
// document, who has never heard of catalog.Template and does not care to.
func readable(err error) string {
	msg := strings.TrimPrefix(err.Error(), "yaml: unmarshal errors:\n")
	msg = strings.TrimPrefix(msg, "yaml: ")
	msg = unknownKeyRe.ReplaceAllString(msg, `there is no "$1" key`)
	msg = goTypeNames.Replace(msg)
	// Several complaints come back one per line, which is a list this will be
	// shown as one item of.
	return strings.TrimSpace(strings.ReplaceAll(msg, "\n", "; "))
}

// YAML writes the catalogue back out in the format Parse reads, which is how a
// catalogue edited entry by entry in the form stays exportable, and how one
// edited as text keeps its shape.
func (c Catalog) YAML() (string, error) {
	out, err := yaml.Marshal(document{Name: c.Name, Categories: c.Categories, Entries: c.Templates})
	return string(out), err
}

// idRe is what an ID has to be: it is proposed as a subdomain, so it has to
// survive as a DNS label.
var idRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// paramRe is what a parameter may be named. The braces around it are the
// substitution syntax, so a name carrying one of its own could never resolve.
var paramRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// placeholderRe finds every {{NAME}} written in an entry.
var placeholderRe = regexp.MustCompile(`\{\{([A-Za-z0-9_]+)\}\}`)

// builtinPlaceholders are the ones Quasar answers itself, which an entry may
// use without declaring anything.
var builtinPlaceholders = []string{"RANDOM", "HOST", "URL"}

// Validate reports everything wrong with a catalogue, rather than the first
// thing — an operator pasting a document in wants the whole list, not one round
// trip per typo.
//
// These are the checks the built-in entries have always been held to by the
// tests in this package, applied at the moment an operator's own is saved,
// because there is no review step between writing one and it appearing on the
// page. What it will not do is second-guess a compose file that parses: an
// unusual stack is the operator's business.
func (c Catalog) Validate() []error {
	var errs []error
	seen := map[string]bool{}
	for _, t := range c.Templates {
		where := t.ID
		if where == "" {
			where = "(entry with no id)"
		}
		add := func(format string, args ...any) {
			errs = append(errs, fmt.Errorf("%s: %s", where, fmt.Sprintf(format, args...)))
		}

		switch {
		case t.ID == "":
			add("an entry needs an id")
		case !idRe.MatchString(t.ID):
			add("id %q is not usable as a subdomain: lowercase letters, digits and hyphens only", t.ID)
		case seen[t.ID]:
			add("two entries share this id")
		}
		seen[t.ID] = true

		if t.Name == "" {
			add("an entry needs a name")
		}
		if t.Description == "" {
			add("an entry needs a description; it is all the card says about it")
		}
		if t.Category == "" {
			add("an entry needs a category, or it will not appear on the page")
		}
		if t.Port <= 0 || t.Port > 65535 {
			add("port %d is out of range", t.Port)
		}

		switch t.Type() {
		case "image":
			if t.ImageRef == "" {
				add("an image entry needs an image_ref")
			}
			if t.Compose != "" {
				add("an image entry carries a compose file; set deploy_type: compose or drop it")
			}
		case "compose":
			if t.ImageRef != "" {
				add("a compose entry carries an image_ref; the images belong in the compose file")
			}
			// Reading an empty file to report that it declares no services
			// says the same thing twice about one missing field.
			if t.Compose == "" {
				add("a compose entry needs a compose file")
			} else if err := validateCompose(t); err != nil {
				add("%s", err)
			}
		default:
			add("deploy_type %q is neither image nor compose", t.DeployType)
		}

		errs = append(errs, validateParams(t, where, c.Scripted)...)
	}

	for _, cat := range c.Categories {
		if cat == "" {
			errs = append(errs, errors.New("a category with no name"))
		}
	}
	return errs
}

// validateCompose checks the file parses and that the service the domain is
// routed to is one the file defines. A compose entry naming a service that is
// not there deploys and then routes nowhere, which looks like a DNS problem.
func validateCompose(t Template) error {
	var f struct {
		Services map[string]yaml.Node `yaml:"services"`
	}
	if err := yaml.Unmarshal([]byte(t.Compose), &f); err != nil {
		return fmt.Errorf("the compose file is not valid YAML: %w", err)
	}
	if len(f.Services) == 0 {
		return errors.New("the compose file declares no services")
	}
	if t.ComposeService != "" {
		if _, ok := f.Services[t.ComposeService]; !ok {
			return fmt.Errorf("compose_service is %q, which the compose file does not define", t.ComposeService)
		}
	}
	return nil
}

// validateParams holds an entry's parameters to what the form can draw and the
// substitution can resolve, and — the check worth having — that every {{NAME}}
// the entry writes is one it declares. A misspelled placeholder is otherwise
// invisible: it survives substitution untouched and reaches the app's .env as
// the literal text "{{VERISON}}".
func validateParams(t Template, where string, scripted bool) []error {
	var errs []error
	add := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf("%s: %s", where, fmt.Sprintf(format, args...)))
	}

	declared := make([]string, 0, len(t.Params))
	for _, p := range t.Params {
		switch {
		case p.Name == "":
			add("a parameter needs a name")
			continue
		case !paramRe.MatchString(p.Name):
			add("parameter %q: a name may hold letters, digits and underscores", p.Name)
		case slices.Contains(builtinPlaceholders, p.Name):
			add("parameter %q: that name is Quasar's own placeholder", p.Name)
		case slices.Contains(declared, p.Name):
			add("parameter %q is declared twice", p.Name)
		}
		declared = append(declared, p.Name)

		switch {
		case p.Type() == "select":
			if len(p.Options) == 0 {
				add("parameter %q offers a choice with no options", p.Name)
			}
		case !slices.Contains(ParamKinds, p.Type()):
			add("parameter %q has kind %q; use one of %s", p.Name, p.Kind, strings.Join(ParamKinds, ", "))
		}

		switch {
		case p.OptionsFrom == "":
		case !scripted:
			add("parameter %q takes its options from %q, and a catalogue entry has nothing to ask; that is a station's field",
				p.Name, p.OptionsFrom)
		case p.Type() != "select":
			add("parameter %q takes its options from %q and is a %s; only a select offers options",
				p.Name, p.OptionsFrom, p.Type())
		}

		// A default that the parameter itself would reject leaves the form
		// proposing a value it will not accept back.
		if p.Default != "" && !p.Accepts(p.Default) {
			add("parameter %q defaults to %q, which it does not accept", p.Name, p.Default)
		}
		if p.Default == "" && p.Type() != "text" {
			add("parameter %q needs a default; the form has nothing else to start from", p.Name)
		}
	}

	// Name is left out: it titles the card in a list of entries, where nothing
	// has been picked yet and a placeholder would be shown as the braces it is.
	if placeholderRe.MatchString(t.Name) {
		add("the name carries a placeholder; the card is drawn before anything is picked, so put it in app_name instead")
	}
	for _, field := range []string{t.AppName, t.Subdomain, t.ImageRef, t.Compose, t.Env} {
		for _, m := range placeholderRe.FindAllStringSubmatch(field, -1) {
			name := m[1]
			if slices.Contains(builtinPlaceholders, name) || slices.Contains(declared, name) {
				continue
			}
			add("uses {{%s}}, which no parameter declares", name)
		}
	}
	return errs
}
