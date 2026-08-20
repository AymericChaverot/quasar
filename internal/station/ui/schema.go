// Package ui is the half of a station document that describes its page: tabs,
// panels, the components they draw and the theme they draw them in.
//
// The rule the whole design rests on is that a station's script never produces
// markup. It returns data, and this schema says which Quasar component that
// data is handed to. There is therefore no HTML to sanitise, and a station
// written against Nebula still looks like Quasar when it lands on a server
// running Solarized.
//
// This file is the schema and its validation. Rendering it lives beside it.
package ui

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// UI is the `ui:` block of a station document.
type UI struct {
	Theme Theme `yaml:"theme,omitempty"`
	Tabs  []Tab `yaml:"tabs,omitempty"`
}

// Tab is one entry in the strip at the top of the station's block.
type Tab struct {
	ID     string  `yaml:"id"`
	Name   string  `yaml:"name"`
	Panels []Panel `yaml:"panels,omitempty"`
}

// Panel is one component. The fields are a union: every panel carries an id, a
// type and the presentation keys, and then whichever keys its type reads. The
// alternative — a type per component and a custom unmarshaller dispatching on
// the type key — buys stricter parsing at the cost of a shape that is far
// harder to read, and Validate below covers the same ground.
type Panel struct {
	ID   string `yaml:"id"`
	Type string `yaml:"type"`

	// Presentation, accepted by every panel.
	Title   string  `yaml:"title,omitempty"`
	Help    string  `yaml:"help,omitempty"`
	Variant string  `yaml:"variant,omitempty"` // hero | plain | inset
	Tone    string  `yaml:"tone,omitempty"`    // accent | ok | warn | err
	Width   string  `yaml:"width,omitempty"`   // full | half | third
	Refresh Refresh `yaml:"refresh,omitempty"`

	// Source is where the panel's data comes from: an action the script
	// exports, or content written into the document.
	Source Source `yaml:"source,omitempty"`

	// Nested panels, for the structural types.
	Panels []Panel `yaml:"panels,omitempty"`

	// table
	Columns    []Column `yaml:"columns,omitempty"`
	RowActions []Action `yaml:"row_actions,omitempty"`
	Empty      string   `yaml:"empty,omitempty"`

	// form
	Fields []Field `yaml:"fields,omitempty"`
	Submit Action  `yaml:"submit,omitempty"`

	// button, confirm and search: one action, reached straight from the panel.
	Label       string `yaml:"label,omitempty"`
	Action      string `yaml:"action,omitempty"`
	Confirm     string `yaml:"confirm,omitempty"`
	Placeholder string `yaml:"placeholder,omitempty"`

	// Long runs a button's or a search's action as a background job; see
	// Action.Long.
	Long bool `yaml:"long,omitempty"`

	// log
	Service string `yaml:"service,omitempty"`
	Tail    int    `yaml:"tail,omitempty"`

	// iframe and image. Src takes {{service:name}}, resolved to an address on
	// the internal network, so a map viewer needs no published port.
	Src    string `yaml:"src,omitempty"`
	Height string `yaml:"height,omitempty"`
}

// Source is a panel's data: `{action: name}` calls the script, `{static: ...}`
// is content written into the document and never fetched.
type Source struct {
	Action string `yaml:"action,omitempty"`
	Static any    `yaml:"static,omitempty"`
}

// Empty reports a panel that declared no source at all, which is what the
// structural components do.
func (s Source) Empty() bool { return s.Action == "" && s.Static == nil }

// Refresh re-fetches a panel on a timer.
type Refresh struct {
	Seconds int `yaml:"seconds,omitempty"`
}

// Column is one column of a table. Key names the field to read out of each row
// the action returned.
type Column struct {
	Key   string `yaml:"key"`
	Label string `yaml:"label"`
	Align string `yaml:"align,omitempty"` // left | right | center
	Type  string `yaml:"type,omitempty"`  // text | badge | code
}

// Action is a button: on a row, or under a form. Confirm is the question asked
// first, with {{key}} resolved against the row.
type Action struct {
	Label   string `yaml:"label"`
	Action  string `yaml:"action"`
	Tone    string `yaml:"tone,omitempty"`
	Confirm string `yaml:"confirm,omitempty"`

	// Long runs this action as a background job with a progress pane, instead
	// of holding a request open for it. Upgrading a server or downloading
	// forty mods does not fit in an HTTP request, and a browser that gave up
	// waiting must not be the thing that cancelled it.
	Long bool `yaml:"long,omitempty"`
}

// Field is one input of a form.
type Field struct {
	Name        string   `yaml:"name"`
	Label       string   `yaml:"label,omitempty"`
	Type        string   `yaml:"type,omitempty"` // see FieldTypes
	Default     string   `yaml:"default,omitempty"`
	Options     []string `yaml:"options,omitempty"`
	Placeholder string   `yaml:"placeholder,omitempty"`
	Help        string   `yaml:"help,omitempty"`
}

// The vocabularies, listed once. A component type spelled out again in the
// validation and a third time in the renderer is how the three come to
// disagree, which is the lesson the catalogue's ParamKinds already carries.
var (
	// StructurePanels hold other panels or nothing at all; they never call the
	// script.
	StructurePanels = []string{"section", "grid", "divider", "banner"}

	// DataPanels render what an action returned.
	DataPanels = []string{"table", "stat", "list", "keyvalue", "markdown", "code", "log", "gauge", "timeline", "image"}

	// InputPanels send something back.
	InputPanels = []string{"form", "button", "search", "confirm"}

	// EmbedPanels point at something else.
	EmbedPanels = []string{"iframe"}

	FieldTypes  = []string{"text", "number", "select", "toggle", "secret", "port", "textarea", "file"}
	ColumnTypes = []string{"text", "badge", "code"}
	Aligns      = []string{"left", "right", "center"}
	Variants    = []string{"hero", "plain", "inset"}
	Tones       = []string{"accent", "ok", "warn", "err"}
	Widths      = []string{"full", "half", "third"}
)

// PanelTypes is every component a panel may be, in the order the families are
// documented.
func PanelTypes() []string {
	return slices.Concat(StructurePanels, DataPanels, InputPanels, EmbedPanels)
}

// LongActions are the ones declared to run as background jobs. It is read
// where an action is about to be run, so that "long" is a property of the
// document rather than of whichever button happened to be pressed.
func (u UI) LongActions() []string {
	var out []string
	add := func(name string, long bool) {
		if long && name != "" && !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	for _, t := range u.Tabs {
		eachPanel(t.Panels, func(p Panel) {
			add(p.Action, p.Long)
			add(p.Submit.Action, p.Submit.Long)
			for _, a := range p.RowActions {
				add(a.Action, a.Long)
			}
		})
	}
	return out
}

// Actions lists every action the interface can reach, in the order it declares
// them: panel sources, row actions, form submits and the standalone buttons.
// A station's validation checks each against what the script exports, because
// a mistyped action is otherwise a panel that renders blank at the moment
// somebody needed it.
func (u UI) Actions() []string {
	var out []string
	add := func(name string) {
		if name != "" && !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	for _, t := range u.Tabs {
		eachPanel(t.Panels, func(p Panel) {
			add(p.Source.Action)
			add(p.Action)
			add(p.Submit.Action)
			for _, a := range p.RowActions {
				add(a.Action)
			}
		})
	}
	return out
}

// Services lists the services the interface names directly — the ones a log
// pane reads. A station's validation holds them to the compose file and to the
// permission that has to cover them.
func (u UI) Services() []string {
	var out []string
	for _, t := range u.Tabs {
		eachPanel(t.Panels, func(p Panel) {
			if p.Service != "" && !slices.Contains(out, p.Service) {
				out = append(out, p.Service)
			}
		})
	}
	return out
}

// Embeds lists the services an iframe points at, with the port each one is
// reached on. A station's validation holds them to the same permission a
// script's own request would need.
func (u UI) Embeds() []ServiceRef {
	var out []ServiceRef
	for _, t := range u.Tabs {
		eachPanel(t.Panels, func(p Panel) {
			if p.Type != "iframe" {
				return
			}
			if ref, ok := ParseServiceSrc(p.Src); ok {
				out = append(out, ref)
			}
		})
	}
	return out
}

// eachPanel walks the panels of a tab, nested ones included.
func eachPanel(panels []Panel, fn func(Panel)) {
	for _, p := range panels {
		fn(p)
		eachPanel(p.Panels, fn)
	}
}

// idRe is what a tab or panel id may be. Panel ids travel in URLs and in the
// refresh lists an action returns, so they are held to a shape that survives
// both.
var idRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// serviceRefRe is the {{service:name:port}} an iframe's src is written with.
var serviceRefRe = regexp.MustCompile(`^\{\{service:([A-Za-z0-9][A-Za-z0-9._-]*):([0-9]{1,5})\}\}(.*)$`)

// ServiceRef is what an iframe points at: one of the application's own
// containers, named, on a port the document declared.
//
// The address itself never appears in the document and never reaches the
// browser. A container's address is not routable from anywhere but this
// server, so the page loads the embed through Quasar, which is also where the
// permission is checked — and no port has to be published to the world for a
// map viewer to be on the page.
type ServiceRef struct {
	Service string
	Port    int
	Path    string
}

// ParseServiceSrc reads an iframe's src, and reports whether it names a
// service at all. Anything else is an ordinary URL and is left alone.
func ParseServiceSrc(src string) (ServiceRef, bool) {
	m := serviceRefRe.FindStringSubmatch(strings.TrimSpace(src))
	if m == nil {
		return ServiceRef{}, false
	}
	port, err := strconv.Atoi(m[2])
	if err != nil || port <= 0 || port > 65535 {
		return ServiceRef{}, false
	}
	path := m[3]
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return ServiceRef{Service: m[1], Port: port, Path: path}, true
}

// Validate reports everything wrong with the interface rather than the first
// thing, the way the catalogue's does: whoever pasted the document wants the
// whole list, not one round trip per typo.
func (u UI) Validate() []error {
	var errs []error
	tabIDs := map[string]bool{}
	panelIDs := map[string]bool{}

	for i, t := range u.Tabs {
		where := fmt.Sprintf("tab %q", t.ID)
		if t.ID == "" {
			where = fmt.Sprintf("tab %d", i+1)
		}
		add := func(format string, args ...any) {
			errs = append(errs, fmt.Errorf("%s: %s", where, fmt.Sprintf(format, args...)))
		}

		switch {
		case t.ID == "":
			add("a tab needs an id")
		case !idRe.MatchString(t.ID):
			add("id %q: lowercase letters, digits, hyphens and underscores only", t.ID)
		case tabIDs[t.ID]:
			add("two tabs share this id")
		}
		tabIDs[t.ID] = true

		if t.Name == "" {
			add("a tab needs a name; it is what the strip says")
		}
		if len(t.Panels) == 0 {
			add("a tab with no panels draws an empty page")
		}
		eachPanel(t.Panels, func(p Panel) {
			errs = append(errs, validatePanel(p, panelIDs)...)
		})
	}

	return append(errs, u.Theme.Validate()...)
}

// validatePanel holds one component to what its type can draw. Panel ids are
// unique across the whole document rather than per tab, because an action
// returning refresh: [mod_list] names one panel and has no tab in hand.
func validatePanel(p Panel, seen map[string]bool) []error {
	var errs []error
	where := fmt.Sprintf("panel %q", p.ID)
	if p.ID == "" {
		where = fmt.Sprintf("a %s panel", p.Type)
	}
	add := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf("%s: %s", where, fmt.Sprintf(format, args...)))
	}

	switch {
	case p.ID == "":
		add("a panel needs an id; it is what a refresh names")
	case !idRe.MatchString(p.ID):
		add("id %q: lowercase letters, digits, hyphens and underscores only", p.ID)
	case seen[p.ID]:
		add("two panels share this id, and a refresh could not say which it meant")
	}
	seen[p.ID] = true

	if !slices.Contains(PanelTypes(), p.Type) {
		add("type %q is not a component; use one of %s", p.Type, strings.Join(PanelTypes(), ", "))
		return errs
	}

	for _, c := range []struct {
		what, value string
		allowed     []string
	}{
		{"variant", p.Variant, Variants},
		{"tone", p.Tone, Tones},
		{"width", p.Width, Widths},
	} {
		if c.value != "" && !slices.Contains(c.allowed, c.value) {
			add("%s %q: use one of %s", c.what, c.value, strings.Join(c.allowed, ", "))
		}
	}
	if p.Refresh.Seconds < 0 {
		add("refresh asks for %d seconds", p.Refresh.Seconds)
	}
	if p.Source.Action != "" && p.Source.Static != nil {
		add("the source is both an action and static content; it is one or the other")
	}

	switch {
	case slices.Contains(DataPanels, p.Type):
		// A log pane reads a container rather than the script, so it is the
		// one data component that needs no source.
		if p.Source.Empty() && p.Type != "log" {
			add("a %s panel has nothing to draw: give it a source", p.Type)
		}
	case slices.Contains(StructurePanels, p.Type):
		if p.Source.Action != "" {
			add("a %s panel draws no data, so an action on it would never run", p.Type)
		}
	}

	switch p.Type {
	case "table":
		if len(p.Columns) == 0 {
			add("a table needs columns")
		}
		for _, c := range p.Columns {
			if c.Key == "" {
				add("a column needs a key; it is the field it reads from each row")
			}
			if c.Align != "" && !slices.Contains(Aligns, c.Align) {
				add("column %q: align %q is not %s", c.Key, c.Align, strings.Join(Aligns, ", "))
			}
			if c.Type != "" && !slices.Contains(ColumnTypes, c.Type) {
				add("column %q: type %q is not %s", c.Key, c.Type, strings.Join(ColumnTypes, ", "))
			}
		}
		errs = append(errs, validateActions(where, p.RowActions)...)
	case "form":
		if len(p.Fields) == 0 {
			add("a form needs fields")
		}
		if p.Submit.Action == "" {
			add("a form needs a submit action; there is nowhere for it to send anything")
		} else {
			errs = append(errs, validateActions(where, []Action{p.Submit})...)
		}
		names := map[string]bool{}
		for _, f := range p.Fields {
			switch {
			case f.Name == "":
				add("a field needs a name; it is the key the action receives")
			case names[f.Name]:
				add("two fields are named %q", f.Name)
			}
			names[f.Name] = true
			if f.Type != "" && !slices.Contains(FieldTypes, f.Type) {
				add("field %q: type %q is not %s", f.Name, f.Type, strings.Join(FieldTypes, ", "))
			}
			// A select may leave its options out when the form is filled by an
			// action, which is then the thing that supplies them — a list of
			// versions, of worlds, of anything that is not knowable when the
			// document is written. With no action there is nothing that could
			// ever fill it, and it is an empty dropdown for good.
			if f.Type == "select" && len(f.Options) == 0 && p.Source.Action == "" {
				add("field %q offers a choice with no options, and no source action to fill them in", f.Name)
			}
		}
	case "button", "confirm", "search":
		if p.Action == "" {
			add("a %s panel needs an action", p.Type)
		}
		if p.Label == "" && p.Type != "search" {
			add("a %s panel needs a label", p.Type)
		}
		if p.Type == "confirm" && p.Confirm == "" {
			add("a confirm panel needs the question to ask")
		}
	case "iframe":
		switch {
		case p.Src == "":
			add("an iframe needs a src")
		case strings.HasPrefix(strings.TrimSpace(p.Src), "{{"):
			if _, ok := ParseServiceSrc(p.Src); !ok {
				add("src %q is not a service reference; write {{service:name:port}} followed by a path", p.Src)
			}
		case !strings.HasPrefix(p.Src, "https://"):
			add("src %q: an iframe reaches one of this application's own services, or an https address", p.Src)
		}
	case "section", "grid":
		if len(p.Panels) == 0 {
			add("a %s holds panels, and this one holds none", p.Type)
		}
	}
	return errs
}

// validateActions checks the buttons of a table or a form.
func validateActions(where string, actions []Action) []error {
	var errs []error
	for _, a := range actions {
		if a.Action == "" {
			errs = append(errs, fmt.Errorf("%s: an action button names no action", where))
		}
		if a.Label == "" {
			errs = append(errs, fmt.Errorf("%s: action %q has no label", where, a.Action))
		}
		if a.Tone != "" && !slices.Contains(Tones, a.Tone) {
			errs = append(errs, fmt.Errorf("%s: action %q: tone %q is not %s", where, a.Action, a.Tone, strings.Join(Tones, ", ")))
		}
	}
	return errs
}

// Length is a CSS length as a document writes it. It exists so that
// `radius_badge: 0` is read as the zero it obviously means: YAML types that as
// a number, and a plain string field would refuse the whole document over it.
type Length string

// UnmarshalYAML accepts any scalar and keeps what was written.
func (l *Length) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.ScalarNode {
		return fmt.Errorf("line %d: a length is one value, like 12px", n.Line)
	}
	*l = Length(n.Value)
	return nil
}
