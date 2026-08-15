package catalog

import (
	"strings"
	"testing"
)

// The entry these exercise: two parameters, one of each interesting kind, used
// everywhere substitution reaches.
func paramTemplate() Template {
	return Template{
		ID: "mc", Name: "Minecraft", Description: "A server", Category: "Game servers", Port: 25565,
		DeployType: "compose", ComposeService: "mc",
		AppName:   "Minecraft {{VERSION}} ({{TYPE}})",
		Subdomain: "mc-{{TYPE}}-{{VERSION}}",
		Params: []Param{
			{Name: "TYPE", Kind: "select", Default: "VANILLA", Options: []string{"VANILLA", "PAPER"}},
			{Name: "VERSION", Default: "1.20.1"},
			{Name: "HOST_PORT", Kind: "port", Default: "25565"},
		},
		Env:     "TYPE={{TYPE}}\nVERSION={{VERSION}}\nHOST_PORT={{HOST_PORT}}\nSECRET={{RANDOM}}",
		Compose: "services:\n  mc:\n    image: itzg/minecraft-server:latest\n",
	}
}

func TestResolveFallsBackToDefaults(t *testing.T) {
	e := paramTemplate()

	got := e.Resolve(nil)
	if got["TYPE"] != "VANILLA" || got["VERSION"] != "1.20.1" {
		t.Errorf("nothing picked should give the declared defaults, got %v", got)
	}

	got = e.Resolve(Values{"TYPE": "PAPER", "VERSION": "1.21"})
	if got["TYPE"] != "PAPER" || got["VERSION"] != "1.21" {
		t.Errorf("picked values did not survive, got %v", got)
	}
}

// The values arrive from a query string, so this is the security-relevant one:
// what an entry does not offer must not be selectable by typing it into the
// address bar, or a select is decoration.
func TestResolveRefusesWhatTheEntryDoesNotOffer(t *testing.T) {
	e := paramTemplate()

	cases := map[string]Values{
		"a value outside a select's options": {"TYPE": "MODDED"},
		"a port that is not a number":        {"HOST_PORT": "nope"},
		"a port out of range":                {"HOST_PORT": "99999"},
		"a value carrying a newline":         {"VERSION": "1.20.1\nEXTRA=surprise"},
	}
	for name, picked := range cases {
		got := e.Resolve(picked)
		for k, v := range picked {
			if got[k] == v {
				t.Errorf("%s was accepted: %s=%q", name, k, v)
			}
		}
	}

	// A parameter the entry never declared is dropped rather than carried
	// through to substitution.
	if got := e.Resolve(Values{"ANYTHING": "x"}); len(got) != len(e.Params) {
		t.Errorf("an undeclared parameter survived Resolve: %v", got)
	}
}

func TestFillSubstitutesEverywhere(t *testing.T) {
	e := paramTemplate()
	v := e.Resolve(Values{"TYPE": "PAPER", "VERSION": "1.21"})

	if got, want := e.SubdomainFor(v), "mc-paper-1-21"; got != want {
		t.Errorf("subdomain = %q, want %q — dots are not legal in a DNS label", got, want)
	}

	f := e.Fill(v, e.SubdomainFor(v), "mc-paper-1-21.example.com")
	if got, want := f.Name, "Minecraft 1.21 (PAPER)"; got != want {
		t.Errorf("app name = %q, want %q", got, want)
	}
	if !strings.Contains(f.Env, "TYPE=PAPER") || !strings.Contains(f.Env, "VERSION=1.21") {
		t.Errorf("parameters did not reach the env:\n%s", f.Env)
	}
	if strings.Contains(f.Env, "{{") {
		t.Errorf("a placeholder survived into the env:\n%s", f.Env)
	}
}

// A value is data, not a template. Resolving what a value happens to contain
// would let a picked version reach into the entry's other parameters.
func TestSubstitutionDoesNotRecurse(t *testing.T) {
	e := paramTemplate()
	v := e.Resolve(Values{"VERSION": "{{TYPE}}"})
	f := e.Fill(v, "x", "x.example.com")
	if !strings.Contains(f.Env, "VERSION={{TYPE}}") {
		t.Errorf("a value containing a placeholder was resolved in turn:\n%s", f.Env)
	}
}

func TestSlugKeepsSubdomainsLegal(t *testing.T) {
	for in, want := range map[string]string{
		"mc-PAPER-1.20.1": "mc-paper-1-20-1",
		"  spaces  ":      "spaces",
		"--dashes--":      "dashes",
		"1.21":            "1-21",
		"":                "",
	} {
		if got := slug(in); got != want {
			t.Errorf("slug(%q) = %q, want %q", in, got, want)
		}
	}
}

// An entry whose subdomain template resolves to nothing still has to propose
// something, or the form arrives with an empty required field.
func TestSubdomainFallsBackToTheID(t *testing.T) {
	e := Template{ID: "mc", Subdomain: "{{VERSION}}", Params: []Param{{Name: "VERSION"}}}
	if got := e.SubdomainFor(e.Resolve(nil)); got != "mc" {
		t.Errorf("subdomain = %q, want the entry ID", got)
	}
}
