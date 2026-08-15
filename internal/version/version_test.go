package version

import (
	"os"
	"regexp"
	"testing"
)

// The image the stack deploys and the image the dashboard offers to update to
// have to be the same string. They are written in two places — docker-compose.yml
// is what actually runs, this package is what the System page reads — and a bump
// applied to only one of them is invisible: the page would either offer an
// update to the version already running, or stay silent about one that is
// waiting. So the file is the source of truth and this test is what binds the
// constant to it.
func TestTraefikImageMatchesCompose(t *testing.T) {
	raw, err := os.ReadFile("../../docker-compose.yml")
	if err != nil {
		t.Fatal(err)
	}
	// The traefik service's image line, whether it is pinned literally or read
	// from TRAEFIK_IMAGE with the tested version as the default.
	re := regexp.MustCompile(`(?m)^\s+image:\s*(?:\$\{TRAEFIK_IMAGE:-)?(traefik:[^}\s]+)\}?\s*$`)
	m := re.FindSubmatch(raw)
	if m == nil {
		t.Fatal("no traefik image line found in docker-compose.yml — has the service been renamed?")
	}
	if got := string(m[1]); got != TraefikImage {
		t.Errorf("docker-compose.yml deploys %q but version.TraefikImage is %q; bump both", got, TraefikImage)
	}
}
