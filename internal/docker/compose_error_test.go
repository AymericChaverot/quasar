package docker

import (
	"fmt"
	"strings"
	"testing"
)

// What a failed stack build actually looks like: a wall of BuildKit progress
// with the one line that matters at the very end. Quoting the whole thing is
// how the reason a deploy failed ends up scrolled off the panel.
func TestComposeTailKeepsTheFailure(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&b, "#%d [frontend builder 1/6] pulling image\n", i)
	}
	b.WriteString("failed to solve: process \"/bin/sh -c npm ci\" did not complete successfully: exit code 1\n")

	got := composeTail([]byte(b.String()))

	if !strings.Contains(got, "npm ci") {
		t.Errorf("the failing line was dropped:\n%s", got)
	}
	if n := len(strings.Split(got, "\n")); n > composeErrLines+1 {
		t.Errorf("kept %d lines, want at most %d plus the omission notice", n, composeErrLines)
	}
	if !strings.Contains(got, "earlier lines omitted") {
		t.Error("truncation must say it happened, or the output reads as the whole story")
	}
}

// Short output is the common case for everything that is not a build — it must
// come through whole, and without the notice.
func TestComposeTailLeavesShortOutputAlone(t *testing.T) {
	out := "no configuration file provided: not found\n"
	if got := composeTail([]byte(out)); got != strings.TrimSpace(out) {
		t.Errorf("composeTail() = %q, want %q", got, strings.TrimSpace(out))
	}
	if composeTail(nil) != "" {
		t.Error("empty output must stay empty")
	}
}
