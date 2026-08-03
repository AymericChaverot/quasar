package docker

import (
	"io"
	"strings"
	"testing"
)

func TestDrainPullReportsInStreamErrors(t *testing.T) {
	tests := []struct {
		name    string
		stream  string
		wantErr string
	}{
		{
			name:   "successful pull",
			stream: `{"status":"Pulling from org/app"}` + "\n" + `{"status":"Download complete"}`,
		},
		{
			// The daemon returns 200 and only then reports that the tag does
			// not exist, so this is the case that used to pass silently.
			name:    "missing tag",
			stream:  `{"status":"Pulling"}` + "\n" + `{"error":"manifest for org/app:v9 not found"}`,
			wantErr: "manifest for org/app:v9 not found",
		},
		{
			name:    "rejected credentials",
			stream:  `{"error":"unauthorized: authentication required"}`,
			wantErr: "unauthorized",
		},
		{
			name:    "truncated stream",
			stream:  `{"status":"Pulling`,
			wantErr: "unexpected EOF",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := drainPull(io.NopCloser(strings.NewReader(tc.stream)), nil)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tc.wantErr != "" && err == nil:
				t.Fatalf("expected an error containing %q", tc.wantErr)
			case tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr):
				t.Fatalf("error %q should contain %q", err, tc.wantErr)
			}
		})
	}
}

// The daemon reports one running total per layer and interleaves the layers, so
// the only figure worth showing is their sum. Extraction repeats the same byte
// counts a second time, which is what would make a pull report 200%.
func TestDrainPullSumsLayerProgress(t *testing.T) {
	stream := strings.Join([]string{
		`{"status":"Pulling from org/app"}`,
		`{"id":"a1","status":"Pulling fs layer"}`,
		`{"id":"b2","status":"Pulling fs layer"}`,
		`{"id":"a1","status":"Downloading","progressDetail":{"current":30,"total":100}}`,
		`{"id":"b2","status":"Downloading","progressDetail":{"current":50,"total":100}}`,
		`{"id":"a1","status":"Downloading","progressDetail":{"current":100,"total":100}}`,
		`{"id":"a1","status":"Download complete"}`,
		`{"id":"b2","status":"Downloading","progressDetail":{"current":100,"total":100}}`,
		`{"id":"b2","status":"Download complete"}`,
		`{"id":"a1","status":"Extracting","progressDetail":{"current":100,"total":100}}`,
		`{"id":"a1","status":"Pull complete"}`,
		`{"status":"Status: Downloaded newer image for org/app:v2"}`,
	}, "\n")

	var last PullStatus
	seen := 0
	err := drainPull(io.NopCloser(strings.NewReader(stream)), func(p PullStatus) {
		last = p
		seen++
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if seen == 0 {
		t.Fatal("the pull reported no progress at all")
	}
	if last.Total != 200 || last.Current != 200 {
		t.Errorf("final progress = %d/%d bytes, want 200/200", last.Current, last.Total)
	}
	if last.Percent != 100 {
		t.Errorf("final percent = %v, want 100", last.Percent)
	}
	if last.Phase != "Extracting" {
		t.Errorf("final phase = %q, want %q", last.Phase, "Extracting")
	}
}

// A pull whose layers the daemon has not sized yet must not be reported as a
// percentage: 0 of 0 bytes is "not known", not "nothing transferred".
func TestDrainPullReportsNoPercentBeforeTheTotalIsKnown(t *testing.T) {
	stream := `{"status":"Pulling from org/app"}` + "\n" + `{"id":"a1","status":"Pulling fs layer"}`
	var last PullStatus
	if err := drainPull(io.NopCloser(strings.NewReader(stream)), func(p PullStatus) { last = p }); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if last.Percent != 0 || last.Total != 0 {
		t.Errorf("progress = %v%% of %d bytes, want 0%% of 0", last.Percent, last.Total)
	}
}

// The tag reaches a shell inside the updater container, and it comes from a
// GitHub release name.
func TestSafeImageRef(t *testing.T) {
	valid := []string{
		"ghcr.io/aymericchaverot/quasar:v1.2.3",
		"ghcr.io/org/app@sha256:abc123",
		"nginx:latest",
	}
	for _, ref := range valid {
		if !safeImageRef(ref) {
			t.Errorf("%q should be accepted", ref)
		}
	}

	invalid := []string{
		"",
		"ghcr.io/org/app:v1;rm -rf /",
		"ghcr.io/org/app:v1 && curl evil.sh",
		"ghcr.io/org/app:$(whoami)",
		"ghcr.io/org/app:`id`",
		"ghcr.io/org/app:v1\nwget evil",
	}
	for _, ref := range invalid {
		if safeImageRef(ref) {
			t.Errorf("%q should be rejected", ref)
		}
	}
}
