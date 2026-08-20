package station

import "testing"

// The install screen is the one place an operator decides whether to run
// somebody else's program on their own machine, and a list of eight sentences
// all starting "Read" is a list nobody reads to the end. Each permission is
// therefore also a shape — and a permission added later that quietly drew as a
// generic box, or as the same shape as another, would take that back.
func TestEveryGrantCarriesAnIconOfItsOwn(t *testing.T) {
	all := Permissions{
		Exec:        Services{Services: []string{"app"}},
		Logs:        Services{Services: []string{"app"}},
		Files:       Files{Paths: []string{"data/**"}},
		Env:         Env{Read: []string{"A"}, Write: []string{"B"}},
		NetInternal: NetInternal{Services: []string{"app"}, Ports: []int{80}},
		NetExternal: NetExternal{Allow: []string{"api.example.com"}},
		Lifecycle:   []string{"restart"},
		Notify:      true,
	}

	grants := all.Summary()
	if len(grants) != 9 {
		t.Fatalf("the summary lists %d permissions; every one this block declares should be in it", len(grants))
	}
	seen := map[string]string{}
	for _, g := range grants {
		if g.Icon == "" {
			t.Errorf("%q has no icon, so it draws as a generic box", g.Title)
			continue
		}
		if other, ok := seen[g.Icon]; ok {
			t.Errorf("%q and %q both draw as %q, so they cannot be told apart", g.Title, other, g.Icon)
		}
		seen[g.Icon] = g.Title
	}
}
