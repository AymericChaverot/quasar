package event

import (
	"bytes"
	"log"
	"testing"
)

// capture sends the package's lines to a buffer, without colour, for the
// length of a test.
func capture(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prevOut, prevColour := out, colour
	out, colour = &buf, false
	t.Cleanup(func() { out, colour = prevOut, prevColour })
	return &buf
}

func TestPrint(t *testing.T) {
	buf := capture(t)
	Info("deploy", "portfolio", "", "deployed in 41s")
	Warning("login", "3 failed attempts")
	Error("backup", "scheduled", "disk full")
	want := "  ✓ deploy      portfolio · deployed in 41s\n" +
		"  ! login       3 failed attempts\n" +
		"  ✗ backup      scheduled · disk full\n"
	if buf.String() != want {
		t.Errorf("got\n%s\nwant\n%s", buf.String(), want)
	}
}

// What the rest of the code still writes with log.Printf comes out as a
// failure, filed under the area its message starts with, and without the
// timestamp the log package would have put in front.
func TestCaptureStandardLog(t *testing.T) {
	buf := capture(t)
	prevFlags, prevOut := log.Flags(), log.Writer()
	t.Cleanup(func() { log.SetFlags(prevFlags); log.SetOutput(prevOut) })
	CaptureStandardLog()

	log.Printf("monitor: recording the sample for %s: %v", "a1", "disk I/O error")
	log.Print("offsite upload of quasar-20260924.tar.gz: timeout")
	log.Print("unprefixed")
	want := "  ✗ monitor     recording the sample for a1: disk I/O error\n" +
		"  ✗ offsite     upload of quasar-20260924.tar.gz: timeout\n" +
		"  ✗ quasar      unprefixed\n"
	if buf.String() != want {
		t.Errorf("got\n%s\nwant\n%s", buf.String(), want)
	}
}
