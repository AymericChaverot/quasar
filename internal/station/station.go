// Package station holds the station document: an application that arrives with
// a control surface of its own.
//
// A catalogue entry says what to deploy. A station says that and what the
// application's page should look like afterwards — tabs, panels and actions
// written for one service in particular, because whoever wrote the station
// knew what running that service involves and Quasar does not.
//
// The `deploy:` block is catalog.Template unchanged, and that is the point
// rather than a convenience: parameter substitution, {{RANDOM}} secrets,
// compose rewriting for Traefik, host port collisions and --env-file handling
// already exist and already have tests, and they apply to a station without a
// line of new code. A station is a catalogue entry with three more blocks
// bolted on, and it should stay readable as one.
//
// What a deployed station produces is an ordinary application: the same db.App
// row, the same containers, logs, storage explorer, backups and TLS, carrying
// one extra field naming the station it came from. A station that is removed
// leaves a perfectly normal application behind.
//
// This package is the document and nothing else — no storage, no server, no
// runtime. Parsing is in parse.go, and everything a document may not say is in
// validate.go.
package station

import (
	"regexp"
	"slices"

	"quasar/internal/catalog"
	"quasar/internal/station/ui"
)

// Schema is the document format this build reads. It is checked from the first
// release, because a format that cannot say which version it is written in
// cannot be changed later without breaking every document already in the wild.
const Schema = 1

// Category is what the deploy block is filed under when it is held to the
// catalogue's own validation. A station never appears in the catalogue — it
// has a page of its own — but Template is one shape with one set of rules, and
// this is the field those rules require.
const Category = "Stations"

// Station is one document, as pasted or fetched.
type Station struct {
	Schema int `yaml:"schema"`

	// ID is unique across the installed stations, and the default subdomain.
	// Unlike a catalogue entry, a station does not override another by reusing
	// its id: a catalogue entry describes third-party software that two people
	// may legitimately both describe, while a station is a program, and
	// silently replacing somebody's program with somebody else's is not a
	// feature.
	ID string `yaml:"id"`

	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Author      string `yaml:"author,omitempty"`

	// Version is the author's, shown on the Stations page and in the diff when
	// a new revision is fetched.
	Version string `yaml:"version"`

	// Deploy is what to run: the catalogue's entry shape, whole.
	Deploy catalog.Template `yaml:"deploy"`

	// Permissions is what the script may reach. Nothing is granted by default.
	Permissions Permissions `yaml:"permissions,omitempty"`

	// UI is what the page shows.
	UI ui.UI `yaml:"ui,omitempty"`

	// Hooks is when the script runs without being asked.
	Hooks Hooks `yaml:"hooks,omitempty"`

	// Script is the JavaScript the actions are exported from.
	Script string `yaml:"script,omitempty"`
}

// Hooks runs actions on the application's own events, and on a timer.
//
// Hooks never block. A failing after_deploy is reported in the deploy panel
// and the audit log; it does not fail the deployment. Third-party code on the
// critical path of a deployment is how a working site goes down for a reason
// nobody can find.
type Hooks struct {
	AfterDeploy  Hook `yaml:"after_deploy,omitempty"`
	OnStart      Hook `yaml:"on_start,omitempty"`
	OnStop       Hook `yaml:"on_stop,omitempty"`
	OnHealthFail Hook `yaml:"on_health_fail,omitempty"`

	// Every runs while the application is running and not otherwise: a stopped
	// server has nothing to poll, and a fleet of stopped applications quietly
	// burning CPU every minute is a bug people discover from their hosting
	// bill.
	Every []Schedule `yaml:"every,omitempty"`
}

// Hook is one action, run on one event.
type Hook struct {
	Action string `yaml:"action,omitempty"`
}

// Schedule is one action on a timer.
type Schedule struct {
	Minutes int    `yaml:"minutes"`
	Action  string `yaml:"action"`
}

// Actions lists every action name the hooks reach, in declaration order.
func (h Hooks) Actions() []string {
	var out []string
	add := func(name string) {
		if name != "" && !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	add(h.AfterDeploy.Action)
	add(h.OnStart.Action)
	add(h.OnStop.Action)
	add(h.OnHealthFail.Action)
	for _, e := range h.Every {
		add(e.Action)
	}
	return out
}

// Empty reports a hooks block that asks for nothing.
func (h Hooks) Empty() bool { return len(h.Actions()) == 0 }

// Template is the deploy block as the catalogue reads it: what the document
// wrote, with the four fields a catalogue entry needs and a station carries at
// the top level instead. This is what the parameter form and the prefilled new
// application form are given, so a station takes the catalogue's path exactly.
func (s Station) Template() catalog.Template {
	t := s.Deploy
	t.ID = s.ID
	t.Name = s.Name
	t.Description = s.Description
	t.Category = Category
	return t
}

// Actions is every action the document reaches, from its interface, from its
// hooks and from the deploy form.
func (s Station) Actions() []string {
	out := s.UI.Actions()
	for _, name := range slices.Concat(s.Hooks.Actions(), s.ChoiceActions()) {
		if !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	return out
}

// ChoiceActions are the ones that fill a deploy parameter's options, in the
// order the parameters declare them.
//
// They are the only actions that run before the application exists, so they
// are the only ones with nothing to act on: no container, no folder, no
// environment, no store. What is left is the way out, which is what a list of
// versions comes from.
func (s Station) ChoiceActions() []string {
	var out []string
	for _, p := range s.Deploy.Params {
		if p.OptionsFrom != "" && !slices.Contains(out, p.OptionsFrom) {
			out = append(out, p.OptionsFrom)
		}
	}
	return out
}

// exportRe finds what the script exports. An action is written as a top-level
// `export function name(...)` and the validation, the runtime and the
// documentation all agree on that one form: a station is meant to be read
// before it is trusted, and a single way of declaring its entry points is part
// of being readable.
var exportRe = regexp.MustCompile(`(?m)^[ \t]*export[ \t]+function[ \t]+([A-Za-z_$][A-Za-z0-9_$]*)[ \t]*\(`)

// Exports lists the functions the script offers, in the order it declares them.
func (s Station) Exports() []string {
	var out []string
	for _, m := range exportRe.FindAllStringSubmatch(s.Script, -1) {
		if !slices.Contains(out, m[1]) {
			out = append(out, m[1])
		}
	}
	return out
}
