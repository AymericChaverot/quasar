package catalog

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// A catalogue entry is data, and data with a typo in it fails at deploy time on
// somebody's server rather than here. These check the shape of every entry.

func TestIDsUniqueAndSlugSafe(t *testing.T) {
	seen := map[string]bool{}
	for _, e := range Templates {
		if seen[e.ID] {
			t.Errorf("duplicate ID %q", e.ID)
		}
		seen[e.ID] = true
		// The ID is prefilled as the subdomain, so it has to survive as one.
		if e.ID != strings.ToLower(e.ID) || strings.ContainsAny(e.ID, " _./") {
			t.Errorf("%s: ID is not usable as a subdomain", e.ID)
		}
	}
}

func TestEveryEntryIsComplete(t *testing.T) {
	for _, e := range Templates {
		if e.Name == "" || e.Description == "" {
			t.Errorf("%s: missing name or description", e.ID)
		}
		if e.Port <= 0 || e.Port > 65535 {
			t.Errorf("%s: port %d out of range", e.ID, e.Port)
		}
		switch e.Type() {
		case "image":
			if e.ImageRef == "" {
				t.Errorf("%s: image deploy with no image", e.ID)
			}
			if e.Compose != "" {
				t.Errorf("%s: image deploy carrying a compose file", e.ID)
			}
		case "compose":
			if e.Compose == "" {
				t.Errorf("%s: compose deploy with no compose file", e.ID)
			}
			if e.ImageRef != "" {
				t.Errorf("%s: compose deploy carrying an image ref", e.ID)
			}
		default:
			t.Errorf("%s: unknown deploy type %q", e.ID, e.DeployType)
		}
	}
}

// Grouped drops anything filed under a category it does not know, so a typo in
// a Category would silently remove the entry from the page.
func TestEveryEntryIsReachableFromGrouped(t *testing.T) {
	var n int
	for _, g := range Builtin().Grouped() {
		n += len(g.Templates)
	}
	if n != len(Templates) {
		t.Errorf("Grouped covers %d of %d templates; check for an unknown Category", n, len(Templates))
	}
}

func TestComposeFilesParseAndDeclareTheirService(t *testing.T) {
	for _, e := range Templates {
		if e.Type() != "compose" {
			continue
		}
		var doc struct {
			Services map[string]struct {
				Image string `yaml:"image"`
			} `yaml:"services"`
		}
		if err := yaml.Unmarshal([]byte(e.Compose), &doc); err != nil {
			t.Errorf("%s: compose file does not parse: %v", e.ID, err)
			continue
		}
		if len(doc.Services) == 0 {
			t.Errorf("%s: compose file declares no services", e.ID)
			continue
		}
		for name, svc := range doc.Services {
			if svc.Image == "" {
				t.Errorf("%s: service %q has no image", e.ID, name)
			}
		}
		if e.ComposeService != "" {
			if _, ok := doc.Services[e.ComposeService]; !ok {
				t.Errorf("%s: routes to service %q, which the file does not define", e.ID, e.ComposeService)
			}
		}
	}
}

// Every ${VAR} a compose file interpolates has to exist in the entry's Env, or
// compose resolves it to the empty string and the stack starts misconfigured.
func TestComposeVarsAreDeclaredInEnv(t *testing.T) {
	for _, e := range Templates {
		if e.Type() != "compose" {
			continue
		}
		declared := map[string]bool{}
		for _, line := range strings.Split(e.Env, "\n") {
			if k, _, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
				declared[strings.TrimSpace(k)] = true
			}
		}
		for _, name := range composeVars(e.Compose) {
			if !declared[name] {
				t.Errorf("%s: compose uses ${%s}, which its Env does not set", e.ID, name)
			}
		}
	}
}

// composeVars lists the ${NAME} references in a compose file.
func composeVars(s string) []string {
	var out []string
	for {
		i := strings.Index(s, "${")
		if i < 0 {
			return out
		}
		s = s[i+2:]
		j := strings.IndexByte(s, '}')
		if j < 0 {
			return out
		}
		out = append(out, s[:j])
		s = s[j+1:]
	}
}

func TestRenderEnvReplacesEveryPlaceholder(t *testing.T) {
	const host = "app.example.com"
	for _, e := range Templates {
		got := e.RenderEnv(host, e.Resolve(nil))
		// Any placeholder left is one the operator would have to notice and
		// fix by hand, which is the thing this is meant to avoid.
		if strings.Contains(got, "{{") {
			t.Errorf("%s: RenderEnv left a placeholder behind:\n%s", e.ID, got)
		}
		if strings.Contains(e.Env, "{{URL}}") && !strings.Contains(got, "https://"+host) {
			t.Errorf("%s: {{URL}} did not resolve to the app's address", e.ID)
		}
		if strings.Contains(e.Env, "{{HOST}}") && !strings.Contains(got, host) {
			t.Errorf("%s: {{HOST}} did not resolve to the app's hostname", e.ID)
		}
	}

	// A CHANGE-ME is only acceptable on an entry that already declares it
	// cannot start without something the operator supplies. Anywhere else it
	// means an app that deploys and then sits there broken.
	for _, e := range Templates {
		if !strings.Contains(e.RenderEnv(host, e.Resolve(nil)), "CHANGE-ME") {
			continue
		}
		if e.NeedsSetup == "" {
			t.Errorf("%s: ships a CHANGE-ME but claims to need no setup; use {{URL}}/{{HOST}} or set NeedsSetup", e.ID)
		}
	}
}

// The entries that cannot come up on their own are the ones the smoke test
// skips, so the reason has to be worth reading — it is all the operator gets.
func TestEntriesNeedingSetupExplainThemselves(t *testing.T) {
	for _, e := range Templates {
		if e.NeedsSetup == "" {
			continue
		}
		if len(e.NeedsSetup) < 40 {
			t.Errorf("%s: NeedsSetup is too terse to act on: %q", e.ID, e.NeedsSetup)
		}
		if !strings.Contains(e.Caveat(), e.NeedsSetup) {
			t.Errorf("%s: NeedsSetup never reaches the page", e.ID)
		}
	}
	// Two selections of the same template must not share a secret.
	v := Builtin().Get("postgres")
	if v == nil {
		t.Fatal("postgres template went missing")
	}
	if v.RenderEnv("a.example.com", nil) == v.RenderEnv("a.example.com", nil) {
		t.Error("RenderEnv returned the same secret twice")
	}
}
