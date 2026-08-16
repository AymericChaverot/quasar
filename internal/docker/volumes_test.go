package docker

import (
	"testing"
	"time"

	"github.com/docker/docker/api/types/volume"
)

func TestVolumeFromReadsUsageAndOwner(t *testing.T) {
	v := volumeFrom(&volume.Volume{
		Name:       "qs-abcd1234_db",
		Driver:     "local",
		Mountpoint: "/var/lib/docker/volumes/qs-abcd1234_db/_data",
		CreatedAt:  "2026-03-04T10:11:12Z",
		Labels:     map[string]string{"com.docker.compose.project": "qs-abcd1234"},
		UsageData:  &volume.UsageData{Size: 4096, RefCount: 2},
	})

	if v.Bytes != 4096 || v.RefCount != 2 || !v.InUse() {
		t.Errorf("usage = %d bytes / %d refs", v.Bytes, v.RefCount)
	}
	if v.AppID != "abcd1234" {
		t.Errorf("AppID = %q, want the app the project belongs to", v.AppID)
	}
	if !v.Local() {
		t.Error("a local volume with a mountpoint should be browsable")
	}
	if v.Created.Year() != 2026 || v.Created.Month() != time.March {
		t.Errorf("Created = %v", v.Created)
	}
}

// The daemon reports -1 when it did not count, which must not read as a volume
// held by minus one container — and so as one still in use.
func TestVolumeFromIgnoresUncountedRefs(t *testing.T) {
	v := volumeFrom(&volume.Volume{
		Name: "orphan", Driver: "local", Mountpoint: "/x",
		UsageData: &volume.UsageData{Size: -1, RefCount: -1},
	})
	if v.RefCount != 0 || v.InUse() {
		t.Errorf("RefCount = %d, InUse = %v, want an orphan", v.RefCount, v.InUse())
	}

	// No usage data at all is the other shape: /volumes fills none of it in.
	bare := volumeFrom(&volume.Volume{Name: "bare", Driver: "local", Mountpoint: "/x"})
	if bare.Bytes != 0 || bare.InUse() {
		t.Errorf("bare volume = %+v", bare)
	}
}

// A volume compose created before the project label existed still names its
// project in its own name, which is the only way left to attribute it.
func TestVolumeFromFallsBackToTheName(t *testing.T) {
	v := volumeFrom(&volume.Volume{Name: "qs-beef0001_pgdata", Driver: "local", Mountpoint: "/x"})
	if v.AppID != "beef0001" {
		t.Errorf("AppID = %q, want it read from the volume name", v.AppID)
	}

	// A volume belonging to nothing Quasar deployed stays unattributed rather
	// than being guessed onto some app.
	for _, name := range []string{"portainer_data", "_leading", "nounderscore"} {
		if got := volumeFrom(&volume.Volume{Name: name}).AppID; got != "" {
			t.Errorf("%s: AppID = %q, want none", name, got)
		}
	}
}

// A volume whose data is not on this machine cannot be opened by reading the
// host filesystem, however it is labelled.
func TestVolumeLocal(t *testing.T) {
	cases := []struct {
		driver, mountpoint string
		want               bool
	}{
		{"local", "/var/lib/docker/volumes/x/_data", true},
		{"local", "", false},
		{"nfs", "/mnt/share", false},
		{"rexray/ebs", "/var/lib/rexray/volumes/x", false},
	}
	for _, c := range cases {
		v := Volume{Driver: c.driver, Mountpoint: c.mountpoint}
		if got := v.Local(); got != c.want {
			t.Errorf("Local(%q, %q) = %v, want %v", c.driver, c.mountpoint, got, c.want)
		}
	}
}

func TestAppIDFromProject(t *testing.T) {
	cases := map[string]string{
		"qs-abcd1234": "abcd1234",
		"qs-":         "",
		"myproject":   "",
		"":            "",
	}
	for project, want := range cases {
		if got := appIDFromProject(project); got != want {
			t.Errorf("appIDFromProject(%q) = %q, want %q", project, got, want)
		}
	}
}

// What has no directory behind it, or no business being offered as one.
func TestSkipMount(t *testing.T) {
	cases := []struct {
		kind, source string
		want         bool
	}{
		{"volume", "/var/lib/docker/volumes/db/_data", false},
		{"bind", "/opt/quasar/apps/abcd1234/data", false},
		// tmpfs lives in memory; there is no host path to open.
		{"tmpfs", "", true},
		{"bind", "", true},
		// A stack that binds the socket has handed its container the daemon.
		// That is not data, and it is not something to put a Browse button on.
		{"bind", "/var/run/docker.sock", true},
		{"bind", "/run/docker.sock", true},
	}
	for _, c := range cases {
		if got := skipMount(c.kind, c.source); got != c.want {
			t.Errorf("skipMount(%q, %q) = %v, want %v", c.kind, c.source, got, c.want)
		}
	}
}
