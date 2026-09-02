package ui

// Turning what an action returned into what a component draws.
//
// The script never produces markup: it returns data, and this file decides
// which of Quasar's components that data is handed to and in what shape. That
// single rule is what makes a station safe to share — there is no HTML to
// sanitise and no injection surface into the dashboard — and what makes one
// look like Quasar, since a table rendered through Quasar's own table inherits
// the theme, the spacing and the empty state for free.
//
// The other half of the job is saying so when the data is the wrong shape. An
// author whose panel renders as a blank card has been told nothing and will
// give up; an author who reads "this table's action returned a string, and a
// table needs a list of rows" has been told exactly what to change.

import (
	"encoding/json"
	"fmt"
	"html/template"
	"quasar/internal/chart"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Result is what an action's function returned.
type Result struct {
	// Data renders the panel that asked for it.
	Data json.RawMessage `json:"data,omitempty"`

	// Toast is a transient message and Warn is one that went less well than it
	// might have; Error is rendered like any Quasar error and makes the action
	// count as failed.
	//
	// Warn exists because the middle case is real and had nowhere to go: a mod
	// installed against a server version its author never tested is neither a
	// success worth a green tick nor a failure, and reporting it as either is a
	// lie the operator acts on.
	Toast string `json:"toast,omitempty"`
	Warn  string `json:"warn,omitempty"`
	Error string `json:"error,omitempty"`

	// Waiting is a source action saying it has nothing yet and expects to.
	// A script is the only thing that knows a game server has started its
	// process but does not answer on its port for another half a minute, and
	// without a way to say so its only options were a lie or a red card.
	Waiting string `json:"waiting,omitempty"`

	// Refresh names panels to re-fetch, Navigate a tab to switch to.
	Refresh  []string `json:"refresh,omitempty"`
	Navigate string   `json:"navigate,omitempty"`

	// Download is a file in the application's own folder to hand to whoever
	// pressed the button — a world archive, an exported configuration, a log
	// somebody wants to send on.
	//
	// A path rather than the bytes: a script that had to carry a two-gigabyte
	// archive through a JavaScript string to give it away would be a script
	// that cannot, and the file is already sitting in a folder Quasar can
	// read. It is held to the same files permission every other read is, so a
	// station hands over what it was allowed to touch and nothing else.
	Download string `json:"download,omitempty"`
}

// ParseResult reads what came back from a call. A script returning something
// that is not an object at all is treated as its data, because
// `return { data: … }` and `return …` are the same intent and refusing the
// second would only teach the ceremony.
func ParseResult(raw json.RawMessage) Result {
	if len(raw) == 0 {
		return Result{}
	}
	var r Result
	if err := json.Unmarshal(raw, &r); err == nil {
		if r.Data != nil || r.Toast != "" || r.Warn != "" || r.Error != "" ||
			r.Waiting != "" || r.Refresh != nil || r.Navigate != "" || r.Download != "" {
			return r
		}
	}
	return Result{Data: raw}
}

// PanelView is one panel ready for its template.
type PanelView struct {
	Panel Panel

	// AppID is what the panel's own refresh and its actions are addressed to.
	AppID string

	// Problem is why there is nothing to draw, in words the author can act on.
	// A panel with one renders as the problem rather than as an empty card.
	Problem string

	// Waiting is why there is nothing to draw yet, for the case that is not a
	// failure at all: the application is still starting, and the panel that
	// reads it cannot read it until it has.
	//
	// It matters that this is a separate field from Problem rather than a
	// nicer wording of one. A red card three seconds after a deploy reads as
	// "you have broken something" to the person who just pressed the button,
	// and the honest answer — nothing is wrong, wait — is a different thing to
	// draw. A panel with one shows a spinner and asks again shortly, so it
	// comes alive on its own instead of waiting for somebody to reload.
	Waiting string

	// One of these, according to the panel's type.
	Rows   []Row
	Stat   Stat
	Pairs  []Pair
	Items  []Item
	Text   string
	Fields []FilledField

	// Stream is where a log pane connects, and Embed where an iframe points —
	// both resolved by the parent, because a container's address means nothing
	// in a browser and a station never gets to write one.
	Stream string
	Embed  string

	// Chart is a positioned chart. Like the two above it is filled in by the
	// parent rather than decoded from what a script returned, because what a
	// chart reads is Quasar's own record of what this station measured.
	Chart chart.View
}

// Row is one line of a table.
type Row struct {
	Cells []Cell

	// Input is the row as an action receives it — every value in it, not only
	// what a column happened to show. A remove button needs the filename, and
	// the filename is often not a column.
	Input string

	// Values are what a confirm question interpolates {{name}} from.
	Values map[string]string
}

// Cell is one field of one row.
type Cell struct {
	Column Column
	Text   string
	Badge  *Badge
}

// Badge is a small state marker: a label and one of Quasar's tones.
type Badge struct {
	Label string
	Tone  string
}

// Stat is one number worth looking at.
type Stat struct {
	Value  string
	Suffix string
	Label  string
}

// Pair is one line of a key–value panel.
type Pair struct {
	Key   string
	Value string
}

// Item is one line of a list.
type Item struct {
	Label string
	Note  string
}

// FilledField is a form field with whatever the source action put in it.
type FilledField struct {
	Field

	// Options shadows the declared ones on purpose: a select whose choices the
	// script computed is the same component drawing a different list, not a
	// second kind of field. It starts as what the document wrote, which is
	// what a form whose action said nothing about it still offers.
	Options []string

	Value   string
	Checked bool
}

// Render decodes what an action returned into what this panel's component
// draws, or into the reason it cannot.
func Render(appID string, p Panel, data json.RawMessage) PanelView {
	v := PanelView{Panel: p, AppID: appID}
	switch p.Type {
	case "table":
		v.Rows, v.Problem = rows(p, data)
	case "stat", "gauge":
		v.Stat, v.Problem = stat(data)
	case "keyvalue":
		v.Pairs, v.Problem = pairs(data)
	case "list", "timeline":
		v.Items, v.Problem = items(data)
	case "markdown", "code", "banner", "image":
		v.Text, v.Problem = text(data)
	case "form":
		v.Fields, v.Problem = fields(p, data)
	}
	return v
}

// Failed is a panel whose action did not come back at all.
func Failed(appID string, p Panel, problem string) PanelView {
	return PanelView{Panel: p, AppID: appID, Problem: problem}
}

// Waiting is a panel that has nothing to show yet and expects to, because
// whatever it reads is still coming up.
func Waiting(appID string, p Panel, why string) PanelView {
	return PanelView{Panel: p, AppID: appID, Waiting: why}
}

// Streaming is a log pane, pointed at the container the parent resolved.
func Streaming(appID string, p Panel, url string) PanelView {
	return PanelView{Panel: p, AppID: appID, Stream: url}
}

// Embedded is an iframe, pointed at the address the parent will proxy.
func Embedded(appID string, p Panel, url string) PanelView {
	return PanelView{Panel: p, AppID: appID, Embed: url}
}

// Charted is a chart, already positioned by the parent from the series it
// read.
func Charted(appID string, p Panel, v chart.View) PanelView {
	return PanelView{Panel: p, AppID: appID, Chart: v}
}

// imageSrcRe is what an image panel may point at when it points at a data:
// URI: an image media type, and a payload with nothing in it that could end an
// attribute. Percent-encoded and base64 are both here, because a script
// drawing its own chart writes the first and one embedding a picture writes
// the second, and neither is more trustworthy than the other.
var imageSrcRe = regexp.MustCompile(`^data:image/[a-zA-Z0-9.+-]+(;[a-zA-Z0-9.+=-]+)*,[^\s"'<>]+$`)

// ImageSrc is what an image panel draws, as a src attribute.
//
// Unlike the theme's icon, this string came back from a station's script, so
// it is checked here rather than trusted: an https address, or an image as a
// data: URI, and nothing else. Anything else draws nothing — a station cannot
// point the page at plain http, at a file: URL, or at a media type that is not
// an image.
//
// Marking it is what makes the component work at all. html/template rewrites
// every data: URI in a src to #ZgotmplZ, so the case this panel exists for —
// a script that computed a picture — is exactly the case that was broken.
func (v PanelView) ImageSrc() template.URL {
	s := strings.TrimSpace(v.Text)
	if strings.HasPrefix(s, "https://") || imageSrcRe.MatchString(s) {
		return template.URL(s)
	}
	return ""
}

// EmbedHeight is how tall an embedded page is drawn, with a workable default
// so a document that said nothing still gets something to look at.
func (v PanelView) EmbedHeight() string {
	if v.Panel.Height != "" {
		return v.Panel.Height
	}
	return "420px"
}

// Empty reports a table that came back with nothing in it, which is a state
// worth drawing rather than a problem.
func (v PanelView) Empty() bool { return v.Problem == "" && len(v.Rows) == 0 }

// EmptyText is what an empty table says, with a plain fallback so a document
// that forgot the line still draws something.
func (v PanelView) EmptyText() string {
	if v.Panel.Empty != "" {
		return v.Panel.Empty
	}
	return "Nothing here."
}

// rows decodes a table.
func rows(p Panel, data json.RawMessage) ([]Row, string) {
	var raw []map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Sprintf("this table's action returned %s; a table needs a list of rows", kind(data))
	}

	out := make([]Row, 0, len(raw))
	for _, r := range raw {
		row := Row{Values: map[string]string{}}
		for k, v := range r {
			row.Values[k] = scalar(v)
		}
		if input, err := json.Marshal(row.Values); err == nil {
			row.Input = string(input)
		}
		for _, col := range p.Columns {
			cell := Cell{Column: col}
			if col.Type == "badge" {
				cell.Badge = badge(r[col.Key])
			} else {
				cell.Text = scalar(r[col.Key])
			}
			row.Cells = append(row.Cells, cell)
		}
		out = append(out, row)
	}
	return out, ""
}

// badge reads a badge cell, which a script writes either as a string or as
// {label, tone}. Nothing at all is a cell with no badge, which is how a table
// marks the rows that have nothing to say.
func badge(v any) *Badge {
	switch b := v.(type) {
	case nil:
		return nil
	case string:
		if b == "" {
			return nil
		}
		return &Badge{Label: b, Tone: ""}
	case map[string]any:
		label := scalar(b["label"])
		if label == "" {
			return nil
		}
		tone := scalar(b["tone"])
		if !slices.Contains(Tones, tone) {
			tone = ""
		}
		return &Badge{Label: label, Tone: tone}
	}
	return &Badge{Label: scalar(v)}
}

// stat decodes one number. A bare value is accepted as well as the full shape:
// `return 3` and `return {value: 3}` mean the same thing to whoever wrote it.
func stat(data json.RawMessage) (Stat, string) {
	var obj map[string]any
	if err := json.Unmarshal(data, &obj); err == nil {
		return Stat{Value: scalar(obj["value"]), Suffix: scalar(obj["suffix"]), Label: scalar(obj["label"])}, ""
	}
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return Stat{}, "this panel's action returned nothing a number could be read from"
	}
	if _, isList := v.([]any); isList {
		return Stat{}, "this stat's action returned a list; a stat shows one value"
	}
	return Stat{Value: scalar(v)}, ""
}

// pairs decodes a key–value panel, from either a list of {key, value} or a
// plain object. Both read naturally in a script and neither is worth refusing.
func pairs(data json.RawMessage) ([]Pair, string) {
	var list []map[string]any
	if err := json.Unmarshal(data, &list); err == nil {
		out := make([]Pair, 0, len(list))
		for _, p := range list {
			out = append(out, Pair{Key: scalar(p["key"]), Value: scalar(p["value"])})
		}
		return out, ""
	}

	var obj map[string]any
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil, fmt.Sprintf("this panel's action returned %s; it needs an object, or a list of {key, value}", kind(data))
	}
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	out := make([]Pair, 0, len(keys))
	for _, k := range keys {
		out = append(out, Pair{Key: k, Value: scalar(obj[k])})
	}
	return out, ""
}

// items decodes a list.
func items(data json.RawMessage) ([]Item, string) {
	var raw []any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Sprintf("this panel's action returned %s; a list needs a list", kind(data))
	}
	out := make([]Item, 0, len(raw))
	for _, v := range raw {
		if obj, ok := v.(map[string]any); ok {
			out = append(out, Item{Label: scalar(obj["label"]), Note: scalar(obj["note"])})
			continue
		}
		out = append(out, Item{Label: scalar(v)})
	}
	return out, ""
}

// text decodes a panel that draws one piece of writing.
func text(data json.RawMessage) (string, string) {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		return s, ""
	}
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return "", "this panel's action returned nothing to show"
	}
	if _, isScalar := v.(map[string]any); isScalar {
		return "", fmt.Sprintf("this panel's action returned %s; it needs text", kind(data))
	}
	return scalar(v), ""
}

// fields fills a form from what its source action returned, leaving the
// declared defaults where it said nothing.
func fields(p Panel, data json.RawMessage) ([]FilledField, string) {
	values := map[string]any{}
	if len(data) > 0 && string(data) != "null" {
		if err := json.Unmarshal(data, &values); err != nil {
			return nil, fmt.Sprintf("this form's action returned %s; a form is filled from an object", kind(data))
		}
	}

	out := make([]FilledField, 0, len(p.Fields))
	for _, f := range p.Fields {
		filled := FilledField{Field: f, Options: f.Options, Value: f.Default}
		if v, ok := values[f.Name]; ok {
			if picked, options, computed := choices(v); computed {
				filled.Options, v = options, picked
			}
			filled.Value = scalar(v)
			if b, isBool := v.(bool); isBool {
				filled.Checked = b
			}
		}
		if f.Type == "toggle" && filled.Value == "true" {
			filled.Checked = true
		}
		out = append(out, filled)
	}
	return out, ""
}

// choices reads a field that came back as its own list of options rather than
// as a bare value: `{version: {value: '1.21.4', options: [...]}}`.
//
// It exists because the interesting lists are not knowable when the document is
// written. Every release of Minecraft there has ever been is a list that grows
// without the station being touched, and a select over a list somebody typed
// out by hand a year ago is a select that is now wrong — while a free text box,
// which is what the alternative comes down to, accepts a version that does not
// exist and turns a typo into a container that will not start.
//
// A value that is not this shape is a value, including an object: only the
// presence of options makes it a choice, so a form filled from an ordinary
// nested object is unaffected.
func choices(v any) (value any, options []string, ok bool) {
	obj, isObj := v.(map[string]any)
	if !isObj {
		return nil, nil, false
	}
	list, hasOptions := obj["options"].([]any)
	if !hasOptions {
		return nil, nil, false
	}
	options = make([]string, 0, len(list))
	for _, item := range list {
		if s := scalar(item); s != "" {
			options = append(options, s)
		}
	}
	return obj["value"], options, true
}

// scalar is a JSON value as one line of text. Numbers keep their shape — 20 is
// "20" and not "20.000000" — because these are read by people.
func scalar(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case json.Number:
		return t.String()
	}
	out, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(out)
}

// kind names what a script returned, in the words its author would use.
func kind(data json.RawMessage) string {
	s := strings.TrimSpace(string(data))
	switch {
	case s == "" || s == "null":
		return "nothing"
	case strings.HasPrefix(s, "["):
		return "a list"
	case strings.HasPrefix(s, "{"):
		return "an object"
	case strings.HasPrefix(s, `"`):
		return "a string"
	case s == "true" || s == "false":
		return "a boolean"
	}
	return "a number"
}

// Interpolate resolves {{name}} in a confirm question against a row, so
// "Remove {{name}}?" asks about the mod somebody clicked next to.
func Interpolate(s string, values map[string]string) string {
	if !strings.Contains(s, "{{") {
		return s
	}
	pairs := make([]string, 0, len(values)*2)
	for k, v := range values {
		pairs = append(pairs, "{{"+k+"}}", v)
	}
	return strings.NewReplacer(pairs...).Replace(s)
}
