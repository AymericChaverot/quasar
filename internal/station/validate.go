package station

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"quasar/internal/catalog"

	"gopkg.in/yaml.v3"
)

// idRe is what a station id has to be: it is proposed as a subdomain, so it
// has to survive as a DNS label.
var idRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// Validate reports everything wrong with a document rather than the first
// thing — whoever pasted it wants the whole list, not one round trip per typo.
//
// The checks worth having are the ones a station cannot fail loudly on its
// own: an action a panel names and the script never exports renders as a blank
// card, a permission naming a service the compose file does not define is a
// grant that covers nothing, and a font fetched from a CDN works perfectly on
// the author's laptop and on nobody's server. All three are caught here,
// before the station is installed, rather than at the moment somebody needed
// it to work.
func (s Station) Validate() []error {
	var errs []error
	add := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf(format, args...))
	}

	switch {
	case s.Schema == 0:
		add("this document does not say which schema it is written in; add `schema: %d` at the top", Schema)
	case s.Schema > Schema:
		add("schema %d is from a later version of Quasar; this one reads schema %d", s.Schema, Schema)
	case s.Schema < Schema:
		add("schema %d is older than anything this Quasar has shipped", s.Schema)
	}

	switch {
	case s.ID == "":
		add("a station needs an id")
	case !idRe.MatchString(s.ID):
		add("id %q is not usable as a subdomain: lowercase letters, digits and hyphens only", s.ID)
	}
	if s.Name == "" {
		add("a station needs a name")
	}
	if s.Description == "" {
		add("a station needs a description; it is all the card says about it")
	}
	if strings.TrimSpace(s.Version) == "" {
		add("a station needs a version; it is what the Stations page shows and what an update is compared against")
	}

	errs = append(errs, s.validateDeploy()...)
	errs = append(errs, s.Permissions.validate()...)
	errs = append(errs, s.validateServiceNames()...)
	errs = append(errs, s.UI.Validate()...)
	errs = append(errs, s.validateHooks()...)
	errs = append(errs, s.validateActions()...)

	if len(s.UI.Tabs) == 0 && s.Hooks.Empty() {
		add("this station has neither tabs nor hooks, which makes it a catalogue entry with extra steps; write one of those instead")
	}
	return errs
}

// validateDeploy holds the deploy block to the catalogue's own rules, by being
// the catalogue entry it is. Everything the catalogue checks — the compose
// file parses, the routed service exists, every {{NAME}} is declared, a select
// offers options — applies to a station unchanged, and reusing it is the only
// way the two do not drift.
func (s Station) validateDeploy() []error {
	var errs []error

	// The four fields a station carries at the top level. Writing one in the
	// deploy block too is not harmful, but it is silently ignored, and a name
	// that does not take effect is worth a sentence.
	for _, f := range []struct{ key, value string }{
		{"id", s.Deploy.ID},
		{"name", s.Deploy.Name},
		{"description", s.Deploy.Description},
		{"category", s.Deploy.Category},
	} {
		if f.value != "" {
			errs = append(errs, fmt.Errorf("deploy: %s belongs at the top level of the document, not in the deploy block", f.key))
		}
	}

	single := catalog.Catalog{Templates: []catalog.Template{s.Template()}}
	for _, err := range single.Validate() {
		// The catalogue files its complaints under the entry id, which here is
		// the station's own and says nothing; what the reader needs to know is
		// which block it came from.
		msg := strings.TrimPrefix(err.Error(), s.ID+": ")
		errs = append(errs, fmt.Errorf("deploy: %s", msg))
	}
	return errs
}

// validateServiceNames checks that every service a permission or a log pane
// names is one the compose file defines. A permission covering a service that
// does not exist grants nothing, and finds out at the moment the action is
// pressed.
//
// Only compose deploys are checked: a single-image application has one
// container and no service names to get wrong.
func (s Station) validateServiceNames() []error {
	if s.Deploy.Type() != "compose" || s.Deploy.Compose == "" {
		return nil
	}
	defined := composeServices(s.Deploy.Compose)
	if len(defined) == 0 {
		return nil // the catalogue's own validation is already saying so
	}

	var errs []error
	check := func(where string, names []string) {
		for _, name := range names {
			if !slices.Contains(defined, name) {
				errs = append(errs, fmt.Errorf("%s names the service %q, which the compose file does not define; it defines %s",
					where, name, strings.Join(defined, ", ")))
			}
		}
	}
	check("permissions: exec", s.Permissions.Exec.Services)
	check("permissions: logs", s.Permissions.Logs.Services)
	check("permissions: net.internal", s.Permissions.NetInternal.Services)

	for _, name := range s.UI.Services() {
		if !slices.Contains(defined, name) {
			errs = append(errs, fmt.Errorf("a log panel reads the service %q, which the compose file does not define; it defines %s",
				name, strings.Join(defined, ", ")))
		} else if !s.Permissions.AllowsLogs(name) {
			errs = append(errs, fmt.Errorf("a log panel reads the service %q, which the logs permission does not cover", name))
		}
	}

	// An embedded page is reached through Quasar, on the application's own
	// network, which is the same reach a script's own request would need and
	// therefore the same permission.
	for _, ref := range s.UI.Embeds() {
		switch {
		case !slices.Contains(defined, ref.Service):
			errs = append(errs, fmt.Errorf("an iframe points at the service %q, which the compose file does not define; it defines %s",
				ref.Service, strings.Join(defined, ", ")))
		case !s.Permissions.AllowsInternal(ref.Service, ref.Port):
			errs = append(errs, fmt.Errorf("an iframe points at %s on port %d, which the net.internal permission does not cover",
				ref.Service, ref.Port))
		}
	}
	return errs
}

// composeServices lists the services a compose file defines, in no particular
// order. A file that does not parse yields nothing: the catalogue's validation
// is reporting that already, and saying it twice about one mistake helps
// nobody.
func composeServices(compose string) []string {
	var f struct {
		Services map[string]yaml.Node `yaml:"services"`
	}
	if err := yaml.Unmarshal([]byte(compose), &f); err != nil {
		return nil
	}
	out := make([]string, 0, len(f.Services))
	for name := range f.Services {
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}

// validateHooks checks the block that runs the script without being asked.
func (s Station) validateHooks() []error {
	var errs []error
	for _, e := range s.Hooks.Every {
		if e.Action == "" {
			errs = append(errs, fmt.Errorf("hooks: a scheduled entry names no action"))
		}
		if e.Minutes < 1 {
			errs = append(errs, fmt.Errorf("hooks: %q is scheduled every %d minutes; the shortest interval is one minute", e.Action, e.Minutes))
		}
	}
	return errs
}

// validateActions is the check this whole file exists for: every action the
// document reaches has to be a function the script exports. A mistyped action
// is otherwise invisible until somebody presses the button, and what they get
// then is a blank card — the failure an author gives up debugging.
func (s Station) validateActions() []error {
	wanted := s.Actions()
	if len(wanted) == 0 {
		return nil
	}
	if strings.TrimSpace(s.Script) == "" {
		return []error{fmt.Errorf("this station reaches %d action(s) and carries no script", len(wanted))}
	}

	exported := s.Exports()
	var errs []error
	for _, name := range wanted {
		if !slices.Contains(exported, name) {
			errs = append(errs, fmt.Errorf("the action %q is not exported by the script; declare it as `export function %s(...)`", name, name))
		}
	}
	return errs
}
