package db

import "testing"

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
