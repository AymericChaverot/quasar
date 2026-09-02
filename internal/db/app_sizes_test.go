package db

import (
	"testing"
	"time"
)

// The whole point of the table is not walking a directory twice. Whatever
// weighs an application records it, and whatever wants a figure reads the
// newest row — so the newest row has to be the newest row, and it has to come
// back in a form a caller can compare against the clock.
func TestTheNewestSampleIsWhatComesBack(t *testing.T) {
	database := openTestDB(t)

	for _, s := range []struct {
		app   string
		at    time.Time
		bytes int64
	}{
		{"app1", time.Now().UTC().Add(-3 * time.Hour), 100},
		{"app1", time.Now().UTC().Add(-1 * time.Hour), 300},
		{"app2", time.Now().UTC().Add(-2 * time.Hour), 700},
	} {
		if _, err := database.Exec("INSERT INTO app_sizes (app_id, ts, bytes) VALUES (?, ?, ?)",
			s.app, s.at.Format("2006-01-02 15:04:05"), s.bytes); err != nil {
			t.Fatal(err)
		}
	}

	latest, err := LatestAppSizes(database)
	if err != nil {
		t.Fatal(err)
	}
	if len(latest) != 2 {
		t.Fatalf("got %d applications, want 2", len(latest))
	}
	if latest["app1"].Bytes != 300 {
		t.Errorf("app1 came back as %d bytes; the newest sample says 300", latest["app1"].Bytes)
	}
	// Compared against time.Now() by the sampler to decide what is due, so a
	// timestamp read back in the wrong zone would have it walking every
	// directory on every pass, or none of them ever.
	if age := time.Since(latest["app1"].TS); age < 30*time.Minute || age > 90*time.Minute {
		t.Errorf("app1's newest sample reads as %v old; it was written an hour ago", age)
	}
}

// A size is a level, not a rate. Averaging an hour hides the import that filled
// the disk and was tidied up before the next sample, which is exactly the event
// somebody opens this graph to find.
func TestABucketKeepsTheHighWaterMark(t *testing.T) {
	database := openTestDB(t)

	base := time.Now().UTC().Truncate(time.Hour).Add(-2 * time.Hour)
	for i, bytes := range []int64{1 << 20, 9 << 20, 2 << 20} {
		if _, err := database.Exec("INSERT INTO app_sizes (app_id, ts, bytes) VALUES ('app1', ?, ?)",
			base.Add(time.Duration(i)*time.Minute).Format("2006-01-02 15:04:05"), bytes); err != nil {
			t.Fatal(err)
		}
	}

	pts, err := AppSizes(database, "app1", base.Add(-time.Minute), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(pts) != 1 {
		t.Fatalf("got %d buckets, want 1", len(pts))
	}
	if pts[0].V1 != 9 {
		t.Errorf("the bucket reads %v MB; the largest sample in it was 9", pts[0].V1)
	}
}

// Deleting an application takes its history with it, or the table grows for
// applications nobody can open any more.
func TestRemovingAnApplicationRemovesItsSizes(t *testing.T) {
	database := openTestDB(t)

	if err := RecordAppSize(database, "app1", 1234); err != nil {
		t.Fatal(err)
	}
	if err := DeleteAppTimeSeries(database, "app1"); err != nil {
		t.Fatal(err)
	}
	if latest, _ := LatestAppSizes(database); len(latest) != 0 {
		t.Errorf("the sizes outlived the application: %v", latest)
	}
}
