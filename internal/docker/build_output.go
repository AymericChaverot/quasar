package docker

import (
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strconv"
	"strings"
)

// The two ways a build says how far through itself it is. The classic builder,
// which is what Quasar asks the daemon for directly, numbers each instruction
// of the Dockerfile; the compose CLI builds through BuildKit, which numbers the
// steps of each stage instead. Between them they are the only count a build
// offers, and so the only thing the bar can move on.
var (
	buildStep    = regexp.MustCompile(`^Step (\d+)/(\d+) :`)
	buildKitStep = regexp.MustCompile(`^#\d+ \[[^\]]*\b(\d+)/(\d+)\]`)
)

// composeStarting matches the lines compose prints once it has finished
// building and is putting the stack's containers up.
var composeStarting = regexp.MustCompile(`\b(?:Container|Network|Volume)\s+\S+\s+(?:Creat|Recreat|Start|Running|Healthy)`)

// buildLineFrac reads how far into a build one of its own lines says it is.
//
// A stage's count is only about that stage, so a multi-stage build restarts it
// several times over. That is why this is reported rather than trusted: the run
// only ever lets the bar move forward, which turns a series of restarting
// counts into a bar that climbs and then waits.
func buildLineFrac(line string) (float64, bool) {
	m := buildStep.FindStringSubmatch(line)
	if m == nil {
		m = buildKitStep.FindStringSubmatch(line)
	}
	if m == nil {
		return 0, false
	}
	n, _ := strconv.Atoi(m[1])
	total, _ := strconv.Atoi(m[2])
	if total <= 0 {
		return 0, false
	}
	// The step is reported as it begins, so what is behind it is n-1 of them:
	// claiming the whole step on its first line would have the bar full while
	// the last and usually longest one was still running.
	return float64(n-1) / float64(total), true
}

// drainBuildOutput consumes the JSON stream returned by ImageBuild, passing on
// what the build printed and how far through the Dockerfile it is, and
// surfaces the daemon's error message if the build failed.
//
// out and step may be nil, for a caller with nobody watching.
func drainBuildOutput(r io.Reader, out func(string), step func(float64)) error {
	dec := json.NewDecoder(r)
	var partial string
	for {
		var msg struct {
			Stream string `json:"stream"`
			Status string `json:"status"`
			Error  string `json:"error"`
		}
		if err := dec.Decode(&msg); err != nil {
			if errors.Is(err, io.EOF) {
				emitBuildLine(partial, out, step)
				return nil
			}
			return err
		}
		if msg.Error != "" {
			emitBuildLine(partial, out, step)
			if out != nil {
				emitBuildLine(msg.Error, out, nil)
			}
			return errors.New(msg.Error)
		}
		// The daemon breaks its narration wherever the buffer happened to end,
		// so a chunk is not a line: it is appended and split on the newlines it
		// actually carries, or "Step 4/9 : RUN make" arrives as three lines and
		// the step number is never seen.
		text := msg.Stream
		if text == "" {
			// A build that has to pull its base image reports that the way a pull
			// does, with a status and no trailing newline of its own.
			if msg.Status == "" {
				continue
			}
			text = msg.Status + "\n"
		}
		partial += text
		for {
			i := strings.IndexByte(partial, '\n')
			if i < 0 {
				break
			}
			emitBuildLine(partial[:i], out, step)
			partial = partial[i+1:]
		}
	}
}

// emitBuildLine passes one finished line on and, when it announces a step,
// reports how far into the Dockerfile it is.
func emitBuildLine(line string, out func(string), step func(float64)) {
	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		return
	}
	if out != nil {
		out(line)
	}
	if step == nil {
		return
	}
	if frac, ok := buildLineFrac(line); ok {
		step(frac)
	}
}
