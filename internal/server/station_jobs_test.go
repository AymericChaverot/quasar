package server

import (
	"bytes"
	"html"
	"strings"
	"testing"
	"time"

	"quasar/internal/station/ui"
)

// A long action's output is readable while it is still running and after the
// request that started it has ended. That is the whole difference between a
// background job and a slow one.
func TestALongActionsOutputOutlivesTheRequest(t *testing.T) {
	var jobs stationJobRegistry
	job, fresh := jobs.start("abcd1234", "upgrade")
	if !fresh {
		t.Fatal("the first start was not fresh")
	}

	// As it happens, not gathered up for the end.
	job.Log("10% fetching the manifest")
	if v := job.view("abcd1234", "upgrade"); !v.Running || len(v.Lines) != 1 {
		t.Fatalf("view = %+v", v)
	}

	job.Log("100% done")
	job.finish(ui.Result{Toast: "Upgraded to 1.21"}, "")

	// And the request that started it is long gone by now.
	v := jobs.get("abcd1234", "upgrade").view("abcd1234", "upgrade")
	if v.Running {
		t.Error("a finished job still reads as running")
	}
	if len(v.Lines) != 2 || v.Toast != "Upgraded to 1.21" {
		t.Errorf("view = %+v", v)
	}
}

// Two upgrades of the same server at once is not something anybody meant to
// ask for. Pressing the button again finds the job already running.
func TestASecondStartFindsTheRunningJob(t *testing.T) {
	var jobs stationJobRegistry
	first, _ := jobs.start("abcd1234", "upgrade")

	second, fresh := jobs.start("abcd1234", "upgrade")
	if fresh {
		t.Error("a second run started while the first was going")
	}
	if second != first {
		t.Error("the second press did not find the running job")
	}

	// Once it is over, the same button starts a new one.
	first.finish(ui.Result{}, "")
	if _, fresh := jobs.start("abcd1234", "upgrade"); !fresh {
		t.Error("a finished job blocked the next run")
	}
}

// Two applications running the same station do not share a job.
func TestJobsAreScopedToTheirApplication(t *testing.T) {
	var jobs stationJobRegistry
	jobs.start("abcd1234", "upgrade")

	if _, fresh := jobs.start("beef0001", "upgrade"); !fresh {
		t.Error("one application's job blocked another's")
	}
}

// The pane keeps polling while there is something to watch, and stops when
// there is not.
func TestTheJobPaneStopsPollingWhenItIsDone(t *testing.T) {
	s := testServer(t)
	draw := func(v stationJobView) string {
		t.Helper()
		var buf bytes.Buffer
		if err := s.pages["app_detail"].ExecuteTemplate(&buf, "station_job", v); err != nil {
			t.Fatal(err)
		}
		return html.UnescapeString(buf.String())
	}

	running := draw(stationJobView{AppID: "abcd1234", Action: "upgrade", Running: true,
		Lines: []string{"10% fetching"}, Elapsed: "3s"})
	if !strings.Contains(running, "/apps/abcd1234/station/job/upgrade") || !strings.Contains(running, "every 1s") {
		t.Errorf("a running job does not keep itself up to date:\n%s", running)
	}
	if !strings.Contains(running, "10% fetching") {
		t.Error("the pane does not show what has been written so far")
	}

	done := draw(stationJobView{AppID: "abcd1234", Action: "upgrade", Toast: "Upgraded", Elapsed: "2m"})
	if strings.Contains(done, "every 1s") {
		t.Errorf("a finished job is still polling:\n%s", done)
	}
	if !strings.Contains(done, "Upgraded") {
		t.Error("the pane does not keep what the action had to say")
	}

	failed := draw(stationJobView{AppID: "abcd1234", Action: "upgrade", Problem: "no build for 1.21"})
	if !strings.Contains(failed, "no build for 1.21") {
		t.Error("the pane does not keep why it failed, which is what it is for")
	}
}

// A job nobody came back for is dropped, so a dashboard left running for a
// year is not also a log server.
func TestFinishedJobsAreSweptEventually(t *testing.T) {
	var jobs stationJobRegistry
	old, _ := jobs.start("abcd1234", "upgrade")
	old.finish(ui.Result{}, "")
	old.started = time.Now().Add(-2 * jobRetention)

	// The sweep happens when there is something to do rather than on a timer.
	jobs.start("beef0001", "something-else")
	if jobs.get("abcd1234", "upgrade") != nil {
		t.Error("a job from an hour ago is still held")
	}
}
