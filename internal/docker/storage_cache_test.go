package docker

import (
	"testing"
	"time"
)

// The System page's disk figures are read once and given out again for a
// moment, for the same applications — not for others, and not once they are
// stale.
func TestStorageIsReusedBriefly(t *testing.T) {
	f := &fakeDaemon{}
	c := fakeClient(t, f)
	ctx := t.Context()

	for range 3 {
		if _, err := c.Storage(ctx, []string{"a", "b"}); err != nil {
			t.Fatal(err)
		}
	}
	if n := f.diskUsages.Load(); n != 1 {
		t.Errorf("three visits read disk usage %d times, want once", n)
	}

	if _, err := c.Storage(ctx, []string{"a"}); err != nil {
		t.Fatal(err)
	}
	if n := f.diskUsages.Load(); n != 2 {
		t.Errorf("another set of applications was given the cached reading")
	}

	c.storageMu.Lock()
	c.storage.at = time.Now().Add(-time.Hour)
	c.storageMu.Unlock()
	if _, err := c.Storage(ctx, []string{"a"}); err != nil {
		t.Fatal(err)
	}
	if n := f.diskUsages.Load(); n != 3 {
		t.Errorf("a stale reading was given out again")
	}

	c.forgetStorage()
	if _, err := c.Storage(ctx, []string{"a"}); err != nil {
		t.Fatal(err)
	}
	if n := f.diskUsages.Load(); n != 4 {
		t.Errorf("a forgotten reading was given out again")
	}
}
