package station

import (
	"strings"
	"testing"
)

// sampleDoc is the smallest document that exercises every block: something to
// deploy, permissions of three different shapes, one tab with one table, a
// scheduled hook and a script exporting exactly what the rest of it reaches.
const sampleDoc = `
schema: 1
id: demo
name: Demo
description: A station the tests can lean on
author: nobody
version: "1.0.0"

deploy:
  deploy_type: compose
  compose_service: app
  port: 8080
  app_name: "Demo {{VERSION}}"
  subdomain: "demo-{{VERSION}}"
  params:
    - {name: VERSION, label: Version, default: "1.0"}
  env: |
    VERSION={{VERSION}}
  compose: |
    services:
      app:
        image: nginx:alpine

permissions:
  exec: {services: [app]}
  logs: {services: [app]}
  files: {paths: ["data/**", "data/config.toml"]}
  env: {read: [VERSION], write: [VERSION]}
  net.internal: {services: [app], ports: [8080]}
  net.external: {allow: ["api.example.com"]}
  lifecycle: [restart, redeploy]
  notify: true

ui:
  theme:
    accent: "#3ba55d"
    tint: 0.06
    radius: 12px
    radius_badge: 0
    case: upper
  tabs:
    - id: main
      name: Main
      panels:
        - id: things
          type: table
          title: Things
          source: {action: list_things}
          refresh: {seconds: 30}
          columns:
            - {key: name, label: Name}
            - {key: state, label: "", type: badge, align: right}
          row_actions:
            - {label: Remove, action: remove_thing, tone: err, confirm: "Remove {{name}}?"}
          empty: "Nothing here yet."
        - id: add
          type: form
          title: Add one
          fields:
            - {name: url, label: URL, placeholder: "https://..."}
          submit: {label: Add, action: add_thing}

hooks:
  after_deploy: {action: sweep}
  every:
    - {minutes: 60, action: sweep}

script: |
  export function list_things() { return { data: [] } }
  export function add_thing({ url }) { return { toast: url } }
  export function remove_thing({ name }) { return { toast: name } }
  export function sweep() {}
`

// parseOK parses a document the tests expect to be accepted, and fails loudly
// with the whole list if it is not.
func parseOK(t *testing.T, doc string) Station {
	t.Helper()
	s, err := Parse(doc)
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	if errs := s.Validate(); len(errs) > 0 {
		t.Fatalf("valid document rejected: %v", errs)
	}
	return s
}

func TestParseReadsAStation(t *testing.T) {
	s := parseOK(t, sampleDoc)

	if s.ID != "demo" || s.Name != "Demo" || s.Version != "1.0.0" {
		t.Errorf("the identity did not survive parsing: %+v", s)
	}
	if !strings.Contains(s.Deploy.Compose, "nginx:alpine") {
		t.Error("the compose file did not survive parsing")
	}
	if len(s.Deploy.Params) != 1 || s.Deploy.Params[0].Name != "VERSION" {
		t.Errorf("the deploy parameters did not survive parsing: %v", s.Deploy.Params)
	}
	if !s.Permissions.AllowsExec("app") || s.Permissions.AllowsExec("other") {
		t.Errorf("exec covers %v", s.Permissions.Exec.Services)
	}
	if !s.Permissions.AllowsLifecycle("restart") || s.Permissions.AllowsLifecycle("stop") {
		t.Errorf("lifecycle covers %v", s.Permissions.Lifecycle)
	}
	if len(s.UI.Tabs) != 1 || len(s.UI.Tabs[0].Panels) != 2 {
		t.Fatalf("the tabs did not survive parsing: %+v", s.UI.Tabs)
	}
	if p := s.UI.Tabs[0].Panels[0]; p.Source.Action != "list_things" || p.Refresh.Seconds != 30 || len(p.Columns) != 2 {
		t.Errorf("the table did not survive parsing: %+v", p)
	}
	if s.Hooks.AfterDeploy.Action != "sweep" || len(s.Hooks.Every) != 1 {
		t.Errorf("the hooks did not survive parsing: %+v", s.Hooks)
	}
}

// The deploy block is catalog.Template, so a station takes the catalogue's own
// path to deployment. What it does not carry there is the four fields it
// already states at the top level, which Template fills in.
func TestTemplateIsTheDeployBlockPlusTheIdentity(t *testing.T) {
	tpl := parseOK(t, sampleDoc).Template()

	if tpl.ID != "demo" || tpl.Name != "Demo" || tpl.Description == "" || tpl.Category != Category {
		t.Errorf("the identity did not reach the template: %+v", tpl)
	}
	if tpl.ComposeService != "app" || tpl.Port != 8080 {
		t.Errorf("the deploy block did not reach the template: %+v", tpl)
	}
}

// A key that is nearly right is the likeliest mistake in a hand-written
// document, and the one YAML would otherwise accept in silence: "permisions"
// parses, grants nothing, and fails on somebody else's server.
func TestParseRejectsAKeyItDoesNotKnow(t *testing.T) {
	_, err := Parse("schema: 1\nid: demo\npermisions:\n  exec: {services: [app]}\n")
	if err == nil {
		t.Fatal("a misspelled key was accepted")
	}
	if !strings.Contains(err.Error(), "permisions") {
		t.Errorf("the error does not name the offending key: %v", err)
	}
	// The message is shown to whoever wrote the document, so it has to be
	// about the document — not about the Go types it was being read into.
	if strings.Contains(err.Error(), "station.") || strings.Contains(err.Error(), "in type") {
		t.Errorf("the error leaks Quasar's own types: %v", err)
	}
}

// A key mistyped inside a block should say which block, because "there is no
// deploytype key" is only half an answer in a document with six of them.
func TestParseNamesTheBlockAKeyWasWrongIn(t *testing.T) {
	_, err := Parse("schema: 1\nid: demo\ndeploy:\n  deploytype: compose\n")
	if err == nil {
		t.Fatal("a misspelled key in the deploy block was accepted")
	}
	if !strings.Contains(err.Error(), "deploy block") {
		t.Errorf("the error does not say where the key was: %v", err)
	}
	if strings.Contains(err.Error(), "catalog.") || strings.Contains(err.Error(), "in type") {
		t.Errorf("the error leaks Quasar's own types: %v", err)
	}
}

// `radius_badge: 0` is how anyone writes a square badge, and YAML types it as
// a number. Refusing the whole document over the missing "px" would be a
// remarkable thing to do.
func TestALengthMayBeWrittenAsANumber(t *testing.T) {
	s := parseOK(t, sampleDoc)
	if s.UI.Theme.RadiusBadge != "0" {
		t.Errorf("radius_badge = %q, want the 0 the document wrote", s.UI.Theme.RadiusBadge)
	}
	if s.UI.Theme.Radius != "12px" {
		t.Errorf("radius = %q", s.UI.Theme.Radius)
	}
}

func TestExportsReadsTheScript(t *testing.T) {
	s := parseOK(t, sampleDoc)
	want := []string{"list_things", "add_thing", "remove_thing", "sweep"}
	got := s.Exports()
	if len(got) != len(want) {
		t.Fatalf("exports = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("exports = %v, want %v", got, want)
			break
		}
	}
}
