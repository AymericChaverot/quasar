package ui

import (
	"encoding/json"
	"strings"
	"testing"
)

var modTable = Panel{
	ID: "mod_list", Type: "table",
	Empty: "No mods installed. Paste a Modrinth link below.",
	Columns: []Column{
		{Key: "name", Label: "Mod"},
		{Key: "version", Label: "Version", Align: "right"},
		{Key: "update", Label: "", Type: "badge"},
	},
}

func TestATableRendersItsRows(t *testing.T) {
	v := Render("abcd1234", modTable, json.RawMessage(`[
		{"name":"Sodium","version":"0.5.8","file":"sodium-0.5.8.jar","update":{"label":"update","tone":"warn"}},
		{"name":"Lithium","version":"0.11.2","file":"lithium.jar","update":null}
	]`))

	if v.Problem != "" {
		t.Fatalf("a well-shaped table was refused: %s", v.Problem)
	}
	if len(v.Rows) != 2 || v.Empty() {
		t.Fatalf("%d rows, empty=%v", len(v.Rows), v.Empty())
	}
	if got := v.Rows[0].Cells[0].Text; got != "Sodium" {
		t.Errorf("first cell = %q", got)
	}
	if b := v.Rows[0].Cells[2].Badge; b == nil || b.Label != "update" || b.Tone != "warn" {
		t.Errorf("badge = %+v", b)
	}
	// A row with nothing to say in a badge column has no badge, rather than an
	// empty one.
	if v.Rows[1].Cells[2].Badge != nil {
		t.Errorf("a null badge rendered as %+v", v.Rows[1].Cells[2].Badge)
	}

	// The row an action receives is the whole row, not only what a column
	// happened to show: a remove button needs the filename, and the filename is
	// not a column here.
	var input map[string]string
	if err := json.Unmarshal([]byte(v.Rows[0].Input), &input); err != nil {
		t.Fatal(err)
	}
	if input["file"] != "sodium-0.5.8.jar" {
		t.Errorf("the row an action would receive is %v", input)
	}
}

// An empty table is a state worth drawing, in the words the document chose.
func TestAnEmptyTableSaysWhatTheDocumentSaid(t *testing.T) {
	v := Render("abcd1234", modTable, json.RawMessage(`[]`))
	if v.Problem != "" || !v.Empty() {
		t.Fatalf("problem = %q, empty = %v", v.Problem, v.Empty())
	}
	if v.EmptyText() != modTable.Empty {
		t.Errorf("empty text = %q", v.EmptyText())
	}

	// And a plain fallback for a document that forgot the line, because a card
	// with nothing in it at all is worse than a dull sentence.
	bare := modTable
	bare.Empty = ""
	if got := Render("abcd1234", bare, json.RawMessage(`[]`)).EmptyText(); got == "" {
		t.Error("an empty table with no declared text says nothing at all")
	}
}

// The failure an author gives up debugging is the blank card. Every wrong shape
// says what it got and what the component needed.
func TestTheWrongShapeIsALegibleProblem(t *testing.T) {
	for _, c := range []struct {
		what  string
		panel Panel
		data  string
		wants []string
	}{
		{"a string where a table was declared", modTable, `"three mods"`,
			[]string{"a string", "list of rows"}},
		{"an object where a table was declared", modTable, `{"name":"Sodium"}`,
			[]string{"an object", "list of rows"}},
		{"a list where a stat was declared", Panel{ID: "p", Type: "stat"}, `[1,2,3]`,
			[]string{"a list", "one value"}},
		{"a string where a key–value panel was declared", Panel{ID: "p", Type: "keyvalue"}, `"nope"`,
			[]string{"a string", "key, value"}},
		{"an object where text was declared", Panel{ID: "p", Type: "markdown"}, `{"a":1}`,
			[]string{"an object", "text"}},
	} {
		v := Render("abcd1234", c.panel, json.RawMessage(c.data))
		if v.Problem == "" {
			t.Errorf("%s: rendered as if it were fine", c.what)
			continue
		}
		for _, want := range c.wants {
			if !strings.Contains(v.Problem, want) {
				t.Errorf("%s: the problem does not mention %q: %s", c.what, want, v.Problem)
			}
		}
	}
}

// A stat is one number, and both ways of writing it mean the same thing to
// whoever wrote it.
func TestAStatAcceptsEitherShape(t *testing.T) {
	full := Render("x", Panel{ID: "p", Type: "stat"}, json.RawMessage(`{"value":3,"suffix":"/ 20"}`))
	if full.Stat.Value != "3" || full.Stat.Suffix != "/ 20" {
		t.Errorf("stat = %+v", full.Stat)
	}
	// And a number keeps its shape: 20, not 20.000000.
	bare := Render("x", Panel{ID: "p", Type: "stat"}, json.RawMessage(`20`))
	if bare.Stat.Value != "20" {
		t.Errorf("stat = %q, want the number as it was written", bare.Stat.Value)
	}
}

func TestAFormIsFilledFromItsSource(t *testing.T) {
	panel := Panel{ID: "props", Type: "form", Fields: []Field{
		{Name: "motd", Label: "MOTD"},
		{Name: "difficulty", Type: "select", Options: []string{"easy", "normal", "hard"}},
		{Name: "pvp", Type: "toggle"},
		{Name: "max_players", Type: "number", Default: "20"},
	}}
	v := Render("x", panel, json.RawMessage(`{"motd":"A server","difficulty":"hard","pvp":true}`))

	if v.Problem != "" {
		t.Fatal(v.Problem)
	}
	if v.Fields[0].Value != "A server" || v.Fields[1].Value != "hard" {
		t.Errorf("fields = %+v", v.Fields)
	}
	if !v.Fields[2].Checked {
		t.Error("a true toggle came back unchecked")
	}
	// A field the action said nothing about keeps the default the document
	// declared, rather than being emptied.
	if v.Fields[3].Value != "20" {
		t.Errorf("max_players = %q, want the declared default", v.Fields[3].Value)
	}
}

// `return { data: … }` and `return …` are the same intent, and refusing the
// second would only teach the ceremony.
func TestParseResultAcceptsBareData(t *testing.T) {
	full := ParseResult(json.RawMessage(`{"toast":"installed","refresh":["mod_list"]}`))
	if full.Toast != "installed" || len(full.Refresh) != 1 {
		t.Errorf("result = %+v", full)
	}

	bare := ParseResult(json.RawMessage(`[{"name":"Sodium"}]`))
	if string(bare.Data) != `[{"name":"Sodium"}]` {
		t.Errorf("data = %s", bare.Data)
	}
}

// A confirm question asks about the row somebody clicked next to.
func TestConfirmInterpolatesTheRow(t *testing.T) {
	got := Interpolate("Remove {{name}}?", map[string]string{"name": "Sodium", "file": "sodium.jar"})
	if got != "Remove Sodium?" {
		t.Errorf("confirm = %q", got)
	}
}
