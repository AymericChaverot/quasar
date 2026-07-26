package monitor

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"quasar/internal/db"
	"quasar/internal/vps"
)

// alertSink stands in for the notification webhook so tests can assert on what
// an operator would actually receive.
type alertSink struct {
	mu   sync.Mutex
	msgs []string
}

func (s *alertSink) taken() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.msgs
	s.msgs = nil
	return out
}

// newAlertHarness wires a database whose notification webhook points at a
// recording server.
func newAlertHarness(t *testing.T) (*sql.DB, *alertSink) {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	sink := &alertSink{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload map[string]string
		json.Unmarshal(body, &payload)
		sink.mu.Lock()
		sink.msgs = append(sink.msgs, payload["content"])
		sink.mu.Unlock()
	}))
	t.Cleanup(srv.Close)

	if err := db.SetSetting(database, db.SettingNotifyURL, srv.URL); err != nil {
		t.Fatal(err)
	}
	return database, sink
}

func TestDiskAlertFiresOnceAndRecovers(t *testing.T) {
	database, sink := newAlertHarness(t)
	al := newAlerter()

	// Disk has no sustain requirement: filling up is not a transient spike.
	al.check(database, vps.Stats{DiskPercent: 91})
	got := sink.taken()
	if len(got) != 1 || !strings.Contains(got[0], "Disk") {
		t.Fatalf("expected one disk alert, got %v", got)
	}

	// Still over: an operator must not be paged every minute.
	al.check(database, vps.Stats{DiskPercent: 93})
	al.check(database, vps.Stats{DiskPercent: 97})
	if got := sink.taken(); len(got) != 0 {
		t.Errorf("expected no repeat alerts while still over, got %v", got)
	}

	// Inside the hysteresis band (threshold 85, band down to 80): not yet
	// recovered, so announcing recovery here would just start flapping.
	al.check(database, vps.Stats{DiskPercent: 83})
	if got := sink.taken(); len(got) != 0 {
		t.Errorf("expected no recovery inside the hysteresis band, got %v", got)
	}

	al.check(database, vps.Stats{DiskPercent: 60})
	got = sink.taken()
	if len(got) != 1 || !strings.Contains(got[0], "back to") {
		t.Fatalf("expected one recovery message, got %v", got)
	}

	// And it can fire again on the next genuine crossing.
	al.check(database, vps.Stats{DiskPercent: 90})
	if got := sink.taken(); len(got) != 1 {
		t.Errorf("expected the alert to re-arm after recovery, got %v", got)
	}
}

func TestCPUAlertNeedsToBeSustained(t *testing.T) {
	database, sink := newAlertHarness(t)
	al := newAlerter()

	// A build or a deploy pins the CPU for a moment; that is not an incident.
	al.check(database, vps.Stats{CPUPercent: 99})
	al.check(database, vps.Stats{CPUPercent: 99})
	if got := sink.taken(); len(got) != 0 {
		t.Fatalf("expected no alert before %d sustained samples, got %v", alertSustain, got)
	}

	al.check(database, vps.Stats{CPUPercent: 99})
	got := sink.taken()
	if len(got) != 1 || !strings.Contains(got[0], "CPU") {
		t.Fatalf("expected a CPU alert on the %d-th sample, got %v", alertSustain, got)
	}
}

// A spike that drops back must reset the counter, or unrelated spikes minutes
// apart would eventually add up to an alert.
func TestSustainCounterResetsOnRecovery(t *testing.T) {
	database, sink := newAlertHarness(t)
	al := newAlerter()

	al.check(database, vps.Stats{CPUPercent: 95})
	al.check(database, vps.Stats{CPUPercent: 10})
	al.check(database, vps.Stats{CPUPercent: 95})
	al.check(database, vps.Stats{CPUPercent: 10})
	al.check(database, vps.Stats{CPUPercent: 95})
	if got := sink.taken(); len(got) != 0 {
		t.Errorf("intermittent spikes should not accumulate into an alert, got %v", got)
	}
}

func TestZeroThresholdDisablesAlert(t *testing.T) {
	database, sink := newAlertHarness(t)
	if err := db.SetSetting(database, db.SettingAlertDisk, "0"); err != nil {
		t.Fatal(err)
	}
	al := newAlerter()

	al.check(database, vps.Stats{DiskPercent: 99})
	al.check(database, vps.Stats{DiskPercent: 99})
	if got := sink.taken(); len(got) != 0 {
		t.Errorf("a 0 threshold must silence the alert, got %v", got)
	}
}

func TestCustomThresholdIsHonoured(t *testing.T) {
	database, sink := newAlertHarness(t)
	if err := db.SetSetting(database, db.SettingAlertDisk, "50"); err != nil {
		t.Fatal(err)
	}
	al := newAlerter()

	// Under the custom threshold but over nothing else.
	al.check(database, vps.Stats{DiskPercent: 45})
	if got := sink.taken(); len(got) != 0 {
		t.Fatalf("45%% is under the configured 50%%, got %v", got)
	}

	al.check(database, vps.Stats{DiskPercent: 55})
	if got := sink.taken(); len(got) != 1 {
		t.Errorf("55%% should trip the configured 50%% threshold, got %v", got)
	}
}

func TestThresholdFallsBackOnGarbage(t *testing.T) {
	database, _ := newAlertHarness(t)
	if err := db.SetSetting(database, db.SettingAlertDisk, "not a number"); err != nil {
		t.Fatal(err)
	}
	if got := threshold(database, db.SettingAlertDisk, AlertDefaultDisk); got != AlertDefaultDisk {
		t.Errorf("threshold = %v, want the %v default", got, AlertDefaultDisk)
	}
}
