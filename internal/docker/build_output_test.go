package docker

import (
	"strings"
	"testing"
)

// The daemon breaks its narration wherever its buffer happened to end, so a
// chunk of the stream is not a line. Passing chunks straight through is how a
// step header arrived split into three and its number was never seen.
func TestDrainBuildOutputRebuildsLinesFromChunks(t *testing.T) {
	stream := `{"stream":"Step 2/4 "}` +
		`{"stream":": RUN make\n"}` +
		`{"stream":" ---> Running in abc123\n ---> def456\n"}`

	var lines []string
	var fracs []float64
	err := drainBuildOutput(strings.NewReader(stream),
		func(l string) { lines = append(lines, l) },
		func(f float64) { fracs = append(fracs, f) })
	if err != nil {
		t.Fatalf("drainBuildOutput() = %v", err)
	}

	want := []string{"Step 2/4 : RUN make", " ---> Running in abc123", " ---> def456"}
	if len(lines) != len(want) {
		t.Fatalf("got %q, want %q", lines, want)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, lines[i], want[i])
		}
	}
	// The second step is beginning, so one of four is behind it.
	if len(fracs) != 1 || fracs[0] != 0.25 {
		t.Errorf("steps reported %v, want [0.25]", fracs)
	}
}

// The build only fails inside the stream; the request that started it succeeded.
func TestDrainBuildOutputSurfacesTheDaemonsError(t *testing.T) {
	stream := `{"stream":"Step 1/1 : RUN false\n"}` +
		`{"error":"The command '/bin/sh -c false' returned a non-zero code: 1"}`

	var lines []string
	err := drainBuildOutput(strings.NewReader(stream), func(l string) { lines = append(lines, l) }, nil)
	if err == nil || !strings.Contains(err.Error(), "non-zero code") {
		t.Fatalf("drainBuildOutput() = %v, want the daemon's message", err)
	}
	// It also has to reach the pane: the error goes into the deployment history,
	// but the pane is where someone is watching.
	if len(lines) == 0 || !strings.Contains(lines[len(lines)-1], "non-zero code") {
		t.Errorf("the failure was not written to the log: %q", lines)
	}
}

func TestDrainBuildOutputToleratesNobodyWatching(t *testing.T) {
	stream := `{"stream":"Step 1/1 : FROM scratch\n"}`
	if err := drainBuildOutput(strings.NewReader(stream), nil, nil); err != nil {
		t.Errorf("drainBuildOutput() = %v, want nil", err)
	}
}

func TestBuildLineFrac(t *testing.T) {
	cases := []struct {
		line string
		want float64
		ok   bool
	}{
		{"Step 1/8 : FROM golang:1.26", 0, true},
		{"Step 8/8 : CMD [\"/app\"]", 0.875, true},
		{"#12 [builder 3/7] RUN go build ./...", 2.0 / 7, true},
		{"#5 [internal] load build definition from Dockerfile", 0, false},
		{"#5 DONE 0.1s", 0, false},
		{" ---> Using cache", 0, false},
		{"", 0, false},
	}
	for _, tc := range cases {
		got, ok := buildLineFrac(tc.line)
		if ok != tc.ok {
			t.Errorf("buildLineFrac(%q) reported %v, want %v", tc.line, ok, tc.ok)
			continue
		}
		if ok && got != tc.want {
			t.Errorf("buildLineFrac(%q) = %v, want %v", tc.line, got, tc.want)
		}
	}
}

// What tells the bar that a stack has stopped building and is coming up.
func TestComposeStarting(t *testing.T) {
	yes := []string{
		" Container qs-ab12-web-1  Creating",
		" Container qs-ab12-web-1  Started",
		" Network qs-ab12_default  Created",
		" Container qs-ab12-db-1  Healthy",
	}
	for _, line := range yes {
		if !composeStarting.MatchString(line) {
			t.Errorf("composeStarting missed %q", line)
		}
	}
	no := []string{
		"#12 [builder 3/7] RUN go build ./...",
		"failed to solve: process \"/bin/sh -c npm ci\" did not complete successfully",
		" => [internal] load metadata for docker.io/library/node",
	}
	for _, line := range no {
		if composeStarting.MatchString(line) {
			t.Errorf("composeStarting matched %q, which is still the build", line)
		}
	}
}
