package db

import (
	"testing"
	"time"
)

// The figures beside the percentages are what the server's cards read a change
// in gigabytes from, so they have to come back as they went in.
func TestAServerSampleKeepsItsLoadSwapAndGigabytes(t *testing.T) {
	database := openTestDB(t)
	in := ServerSample{CPU: 12, Mem: 40, Disk: 70, Load1: 0.82, SwapGB: 0.5, MemGB: 6.4, DiskGB: 56}
	if err := RecordServerMetric(database, in); err != nil {
		t.Fatal(err)
	}
	var out ServerSample
	if err := database.QueryRow(`SELECT cpu, mem, disk, load1, swap_gb, mem_gb, disk_gb FROM metrics`).
		Scan(&out.CPU, &out.Mem, &out.Disk, &out.Load1, &out.SwapGB, &out.MemGB, &out.DiskGB); err != nil {
		t.Fatal(err)
	}
	if out != in {
		t.Errorf("stored %+v, read back %+v", in, out)
	}
}

// A bucket keeps its lowest and highest sample beside its average, so a spike
// inside it is still there to draw.
func TestABucketKeepsItsLowestAndHighestSample(t *testing.T) {
	database := openTestDB(t)
	for _, cpu := range []float64{10, 90, 20} {
		if _, err := database.Exec(`INSERT INTO metrics (ts, cpu, mem, disk) VALUES (datetime('now', '-30 seconds'), ?, ?, 50)`,
			cpu, cpu/2); err != nil {
			t.Fatal(err)
		}
	}
	pts, err := ServerMetrics(database, time.Now().Add(-time.Hour), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(pts) != 1 {
		t.Fatalf("got %d buckets, want 1", len(pts))
	}
	p := pts[0]
	if p.V1 != 40 || p.Lo1 != 10 || p.Hi1 != 90 || p.Lo2 != 5 || p.Hi2 != 45 {
		t.Errorf("bucket = %+v, want an average of 40 between 10 and 90, memory 5 to 45", p)
	}
}
