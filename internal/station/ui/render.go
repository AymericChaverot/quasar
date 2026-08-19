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

	// Refresh names panels to re-fetch, Navigate a tab to switch to.
	Refresh  []string `json:"refresh,omitempty"`
	Navigate string   `json:"navigate,omitempty"`
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
		if r.Data != nil || r.Toast != "" || r.Warn != "" || r.Error != "" || r.Refresh != nil || r.Navigate != "" {
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

// Streaming is a log pane, pointed at the container the parent resolved.
func Streaming(appID string, p Panel, url string) PanelView {
	return PanelView{Panel: p, AppID: appID, Stream: url}
}

// Embedded is an iframe, pointed at the address the parent will proxy.
func Embedded(appID string, p Panel, url string) PanelView {
	return PanelView{Panel: p, AppID: appID, Embed: url}
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
		filled := FilledField{Field: f, Value: f.Default}
		if v, ok := values[f.Name]; ok {
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
