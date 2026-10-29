package docker

import (
	"math"
	"testing"
	"time"
)

func TestCPUPercent(t *testing.T) {
	prev := cpuSample{total: 1_000, system: 100_000, cpus: 4}
	cur := cpuSample{total: 6_000, system: 200_000, cpus: 4}
	// 5,000 of the 100,000 the system spent, on four CPUs: 20% of one.
	if got := cpuPercent(prev, cur); math.Abs(got-20) > 1e-9 {
		t.Errorf("cpuPercent = %v, want 20", got)
	}
	// A counter that went backwards is a different container, not a negative
	// load.
	if got := cpuPercent(cur, prev); got != 0 {
		t.Errorf("cpuPercent going backwards = %v, want 0", got)
	}
}

func TestSwapCPUSample(t *testing.T) {
	c := &Client{}
	now := time.Now()
	if _, ok := c.swapCPUSample("a", cpuSample{system: 1, at: now}); ok {
		t.Fatal("a first reading has nothing to compare against")
	}
	prev, ok := c.swapCPUSample("a", cpuSample{system: 2, at: now.Add(5 * time.Second)})
	if !ok || prev.system != 1 {
		t.Fatalf("second reading: got %+v, %v; want the first one", prev, ok)
	}
	if _, ok := c.swapCPUSample("a", cpuSample{system: 3, at: now.Add(time.Hour)}); ok {
		t.Error("a reading an hour old was compared against")
	}
}
