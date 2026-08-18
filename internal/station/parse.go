package station

import (
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Parse reads a station document.
//
// KnownFields is on, for the reason the catalogue turns it on: a key that is
// nearly right is the likeliest mistake in a hand-written document, and it is
// the one YAML would otherwise swallow in silence. `permisions:` would parse
// cleanly and install a station that had been granted nothing, which the
// author would discover as a runtime error on somebody else's server.
//
// A document that parses is not yet a document that is accepted; see Validate.
func Parse(doc string) (Station, error) {
	var s Station
	dec := yaml.NewDecoder(strings.NewReader(doc))
	dec.KnownFields(true)
	if err := dec.Decode(&s); err != nil {
		return Station{}, fmt.Errorf("this is not a station Quasar can read: %s", readable(err))
	}
	return s, nil
}

// unknownKeyRe matches the decoder's way of reporting a key it does not know.
var unknownKeyRe = regexp.MustCompile(`field (\S+) not found`)

// goTypeNames turns the Go types the decoder names into the parts of a
// document somebody actually wrote.
var goTypeNames = strings.NewReplacer(
	" in type station.Station", " at the top level",
	" in type catalog.Template", " in the deploy block",
	" in type catalog.Param", " on a deploy parameter",
	" in type station.Permissions", " in the permissions block",
	" in type station.Services", " on that permission",
	" in type station.Files", " on the files permission",
	" in type station.Env", " on the env permission",
	" in type station.NetInternal", " on the net.internal permission",
	" in type station.NetExternal", " on the net.external permission",
	" in type station.Hooks", " in the hooks block",
	" in type station.Hook", " on a hook",
	" in type station.Schedule", " on a scheduled action",
	" in type ui.UI", " in the ui block",
	" in type ui.Theme", " in the theme",
	" in type ui.Font", " on a font",
	" in type ui.Tab", " on a tab",
	" in type ui.Panel", " on a panel",
	" in type ui.Source", " on a panel source",
	" in type ui.Refresh", " on a refresh",
	" in type ui.Column", " on a table column",
	" in type ui.Action", " on an action button",
	" in type ui.Field", " on a form field",
)

// leftoverTypeRe catches a type this file has not been taught to name yet.
// Saying nothing about where the key was is worse than saying "on a panel",
// and far better than naming a Go type at somebody who has never heard of one.
var leftoverTypeRe = regexp.MustCompile(` in type \S+`)

// readable rewrites the decoder's message into something worth showing.
// yaml.v3 reports a misspelled key as `field permisions not found in type
// station.Station` — accurate, and no help at all to whoever wrote the
// document, who has never heard of station.Station and does not care to.
func readable(err error) string {
	msg := strings.TrimPrefix(err.Error(), "yaml: unmarshal errors:\n")
	msg = strings.TrimPrefix(msg, "yaml: ")
	msg = unknownKeyRe.ReplaceAllString(msg, `there is no "$1" key`)
	msg = goTypeNames.Replace(msg)
	msg = leftoverTypeRe.ReplaceAllString(msg, "")
	// Several complaints come back one per line, which is a list this will be
	// shown as one item of.
	return strings.TrimSpace(strings.ReplaceAll(msg, "\n", "; "))
}
