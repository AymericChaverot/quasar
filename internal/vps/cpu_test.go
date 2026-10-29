package vps

import (
	"math"
	"testing"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
)

func TestBusyPercent(t *testing.T) {
	t1 := cpu.TimesStat{User: 10, System: 10, Idle: 80}
	t2 := cpu.TimesStat{User: 30, System: 20, Idle: 150}
	// 30 busy out of 100 elapsed.
	if got := busyPercent(t1, t2); math.Abs(got-30) > 1e-9 {
		t.Errorf("busyPercent = %v, want 30", got)
	}
	if got := busyPercent(t2, t1); got != 0 {
		t.Errorf("counters going backwards = %v, want 0", got)
	}
}

// Only a reading with nothing recent to compare against waits; the next one
// is taken against it at once, and one a moment later is given the same
// answer rather than a sliver's worth of noise.
func TestCPUMeterWaitsOnlyWhenCold(t *testing.T) {
	readings := []cpu.TimesStat{
		{User: 0, Idle: 0},
		{User: 50, Idle: 50},  // after the cold wait: 50%
		{User: 60, Idle: 140}, // against the previous: 10%
	}
	n, slept := 0, 0
	m := &cpuMeter{
		times: func() (cpu.TimesStat, error) { r := readings[n]; n++; return r, nil },
		sleep: func(time.Duration) { slept++ },
	}

	if got, _ := m.read(); got != 50 || slept != 1 {
		t.Fatalf("cold read = %v after %d waits, want 50 after 1", got, slept)
	}
	if got, _ := m.read(); got != 50 || n != 2 {
		t.Errorf("a read a moment later = %v with %d readings taken, want the same 50 and no new reading", got, n)
	}
	m.lastAt = m.lastAt.Add(-2 * time.Second)
	if got, _ := m.read(); math.Abs(got-10) > 1e-9 || slept != 1 {
		t.Errorf("warm read = %v after %d waits, want 10 with no new wait", got, slept)
	}
	m.lastAt = m.lastAt.Add(-time.Hour)
	readings = append(readings, cpu.TimesStat{User: 60, Idle: 140}, cpu.TimesStat{User: 70, Idle: 150})
	if got, _ := m.read(); got != 50 || slept != 2 {
		t.Errorf("a read after an hour = %v after %d waits, want a fresh pair (50) after 2", got, slept)
	}
}
