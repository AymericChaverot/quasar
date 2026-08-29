package station

import (
	"strings"
	"testing"
)

// rejected parses a document the tests expect Validate to refuse, and returns
// its complaints joined, so a case can assert on the words the operator reads.
func rejected(t *testing.T, doc string) string {
	t.Helper()
	s, err := Parse(doc)
	if err != nil {
		return err.Error()
	}
	errs := s.Validate()
	if len(errs) == 0 {
		t.Fatal("this document was accepted, and should not have been")
	}
	var b strings.Builder
	for _, err := range errs {
		b.WriteString(err.Error())
		b.WriteString("\n")
	}
	return b.String()
}

// Every rejection names the thing that was wrong. A validation that reports
// "invalid document" is a validation somebody bisects their own file against.
func TestValidateNamesWhatIsWrong(t *testing.T) {
	cases := []struct {
		name    string
		old     string
		new     string
		wants   []string
		wantNot string
	}{{
		name:  "no schema",
		old:   "schema: 1\n",
		new:   "",
		wants: []string{"schema"},
	}, {
		name:  "a schema from the future",
		old:   "schema: 1",
		new:   "schema: 2",
		wants: []string{"later version"},
	}, {
		name:  "an id that is not a subdomain",
		old:   "id: demo",
		new:   "id: Demo Station",
		wants: []string{"subdomain"},
	}, {
		name:  "an action the script never exports",
		old:   "action: list_things",
		new:   "action: list_thing",
		wants: []string{"list_thing", "is not exported by the script"},
	}, {
		// The form is drawn by asking the script, so a parameter naming a
		// function nobody wrote is a dropdown with one value in it and no sign
		// of why.
		name:  "options from an action the script never exports",
		old:   `- {name: VERSION, label: Version, default: "1.0"}`,
		new:   `- {name: VERSION, label: Version, kind: select, default: "1.0", options: ["1.0"], options_from: releases}`,
		wants: []string{"releases", "is not exported by the script"},
	}, {
		name:  "options from an action on a field that offers no choice",
		old:   `- {name: VERSION, label: Version, default: "1.0"}`,
		new:   `- {name: VERSION, label: Version, default: "1.0", options_from: list_things}`,
		wants: []string{"only a select offers options"},
	}, {
		name:  "a permission on a service the compose file does not define",
		old:   "exec: {services: [app]}",
		new:   "exec: {services: [ap]}",
		wants: []string{`"ap"`, "the compose file does not define"},
	}, {
		name:  "a wildcard host",
		old:   `allow: ["api.example.com"]`,
		new:   `allow: ["*.example.com"]`,
		wants: []string{"wildcard"},
	}, {
		name:  "a host with a scheme",
		old:   `allow: ["api.example.com"]`,
		new:   `allow: ["https://api.example.com"]`,
		wants: []string{"without a scheme"},
	}, {
		name:  "a files path that climbs out",
		old:   `paths: ["data/**", "data/config.toml"]`,
		new:   `paths: ["../../etc/**"]`,
		wants: []string{"climbs out"},
	}, {
		name:  "an absolute files path",
		old:   `paths: ["data/**", "data/config.toml"]`,
		new:   `paths: ["/etc/passwd"]`,
		wants: []string{"leaves the application's own folder"},
	}, {
		name:  "an env key that is not one",
		old:   "read: [VERSION]",
		new:   "read: [VER SION]",
		wants: []string{"is not an environment key"},
	}, {
		name:  "a lifecycle verb that does not exist",
		old:   "lifecycle: [restart, redeploy]",
		new:   "lifecycle: [nuke]",
		wants: []string{"nuke", "the verbs are"},
	}, {
		name:  "an identity in the deploy block",
		old:   "deploy:\n  deploy_type: compose",
		new:   "deploy:\n  name: Somewhere else\n  deploy_type: compose",
		wants: []string{"belongs at the top level"},
	}, {
		name:  "a font fetched from a CDN",
		old:   "  theme:\n",
		new:   "  theme:\n    font_display: {family: Minecraftia, src: \"https://fonts.example.com/m.woff2\"}\n",
		wants: []string{"fonts.example.com", "data: URI"},
	}, {
		name:  "an accent that is not a colour",
		old:   `accent: "#3ba55d"`,
		new:   "accent: green",
		wants: []string{"not a hex colour"},
	}, {
		name:  "two panels sharing an id",
		old:   "- id: add",
		new:   "- id: things",
		wants: []string{"share this id"},
	}, {
		name:  "a component that does not exist",
		old:   "type: table",
		new:   "type: tabel",
		wants: []string{"tabel", "is not a component"},
	}, {
		name:  "a chart shape that does not exist",
		old:   "kind: area",
		new:   "kind: zigzag",
		wants: []string{"zigzag", "line, area, bar, stacked"},
	}, {
		name:  "a window nobody can read",
		old:   "range: 7d",
		new:   "range: 7 weeks",
		wants: []string{"7 weeks", "24h or 30d"},
	}, {
		// The name is what the script records under. A document and a script
		// that disagree about it leave a chart permanently empty, so the
		// disagreement is caught at import rather than discovered later.
		name:  "a series named something a script could not record",
		old:   "source: {series: [things_seen]}",
		new:   "source: {series: [Things Seen]}",
		wants: []string{"Things Seen", "lowercase letters"},
	}, {
		name:  "a series on a component that cannot draw one",
		old:   "source: {action: list_things}",
		new:   "source: {series: [things_seen]}",
		wants: []string{"only a chart can"},
	}, {
		name:  "a column with no key",
		old:   "{key: name, label: Name}",
		new:   "{label: Name}",
		wants: []string{"a column needs a key"},
	}, {
		name:  "a form with nowhere to submit",
		old:   "submit: {label: Add, action: add_thing}",
		new:   "submit: {label: Add}",
		wants: []string{"a form needs a submit action"},
	}, {
		name:  "a schedule with no interval",
		old:   "{minutes: 60, action: sweep}",
		new:   "{minutes: 0, action: sweep}",
		wants: []string{"shortest interval"},
	}, {
		name:  "a compose file routing to a service it does not define",
		old:   "compose_service: app",
		new:   "compose_service: web",
		wants: []string{"deploy:", "compose_service"},
	}, {
		name:  "a placeholder no parameter declares",
		old:   `app_name: "Demo {{VERSION}}"`,
		new:   `app_name: "Demo {{VERISON}}"`,
		wants: []string{"deploy:", "VERISON"},
	}}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			doc := strings.Replace(sampleDoc, c.old, c.new, 1)
			if doc == sampleDoc {
				t.Fatalf("the case did not change anything: %q is not in the sample", c.old)
			}
			got := rejected(t, doc)
			for _, want := range c.wants {
				if !strings.Contains(got, want) {
					t.Errorf("the complaint does not mention %q:\n%s", want, got)
				}
			}
			if strings.Contains(got, "in type") {
				t.Errorf("the complaint leaks Quasar's own types:\n%s", got)
			}
		})
	}
}

// A document with a deploy block and nothing else is a catalogue entry written
// the long way round. Saying so is friendlier than installing it and leaving
// the operator to wonder where the tabs went.
func TestAStationNeedsSomethingToShowOrToDo(t *testing.T) {
	doc := `
schema: 1
id: bare
name: Bare
description: Deploys and nothing more
version: "1.0.0"
deploy:
  deploy_type: image
  image_ref: nginx:alpine
  port: 80
`
	if got := rejected(t, doc); !strings.Contains(got, "catalogue entry") {
		t.Errorf("the complaint does not say what this should have been:\n%s", got)
	}
}

// A log pane reads a container, so it needs the permission that reads
// containers. Rendering the tab and discovering that at the moment somebody
// opened it is the worse half of the two places this can be caught.
func TestALogPanelNeedsTheLogsPermission(t *testing.T) {
	doc := strings.Replace(sampleDoc, "  logs: {services: [app]}\n", "", 1)
	doc = strings.Replace(doc,
		"        - id: add\n",
		"        - {id: out, type: log, service: app}\n        - id: add\n", 1)

	if got := rejected(t, doc); !strings.Contains(got, "the logs permission does not cover") {
		t.Errorf("the complaint does not name the missing permission:\n%s", got)
	}
}

// The whole permission model rests on a station reaching only what it declared,
// so the empty case is worth pinning: no block, no capability.
func TestNoPermissionsBlockGrantsNothing(t *testing.T) {
	var p Permissions
	if p.AllowsExec("app") || p.AllowsLogs("app") || p.AllowsLifecycle("restart") {
		t.Error("an undeclared permission granted something")
	}
	if errs := p.validate(); len(errs) > 0 {
		t.Errorf("an empty permissions block was refused: %v", errs)
	}
}
