package docker

import (
	"testing"
	"time"
)

// The daemon prefixes each line with an RFC3339Nano instant when asked for
// timestamps. Peeling it off is what separates when a line was written from
// when Quasar happened to read it.
func TestSplitTimestamp(t *testing.T) {
	tests := []struct {
		name     string
		line     string
		wantText string
		wantTS   string // RFC3339Nano, "" for an unstamped line
	}{
		{
			name:     "the shape the daemon writes",
			line:     "2026-07-28T23:31:01.123456789Z starting server",
			wantText: "starting server",
			wantTS:   "2026-07-28T23:31:01.123456789Z",
		},
		{
			name:     "a message containing spaces",
			line:     "2026-07-28T23:31:01Z GET /index.html 200 12ms",
			wantText: "GET /index.html 200 12ms",
			wantTS:   "2026-07-28T23:31:01Z",
		},
		{
			// A line whose first word is not a timestamp keeps that word: losing
			// it would silently eat the start of every message if the daemon
			// ever stopped prefixing.
			name:     "no timestamp at all",
			line:     "starting server",
			wantText: "starting server",
		},
		{
			name:     "first word only looks like one",
			line:     "2026-07-28 23:31:01 starting server",
			wantText: "2026-07-28 23:31:01 starting server",
		},
		{
			name:     "empty line",
			line:     "",
			wantText: "",
		},
		{
			// A timestamp with nothing after it is a blank line the container
			// printed, and stays one.
			name:     "stamped blank line",
			line:     "2026-07-28T23:31:01Z ",
			wantText: "",
			wantTS:   "2026-07-28T23:31:01Z",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := splitTimestamp(tc.line)
			if got.Text != tc.wantText {
				t.Errorf("Text = %q, want %q", got.Text, tc.wantText)
			}
			if tc.wantTS == "" {
				if !got.TS.IsZero() {
					t.Errorf("TS = %s, want the zero time", got.TS)
				}
				return
			}
			want, err := time.Parse(time.RFC3339Nano, tc.wantTS)
			if err != nil {
				t.Fatal(err)
			}
			if !got.TS.Equal(want) {
				t.Errorf("TS = %s, want %s", got.TS, want)
			}
		})
	}
}
