package vps

import (
	"math"
	"runtime"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
)

// The host's CPU use, as the share of the time since the previous reading.
//
// It was sampled over half a second on each request, so every poll of the
// dashboard's health cards — every five seconds, in every tab it was open in —
// held its answer back for that long. The previous reading is kept instead and
// the delta taken against it: the monitor reads it once a minute, which keeps a
// previous reading always at hand, and only a reading with none recent enough
// waits to take two.

// cpuMinGap is the shortest interval a figure is worked out over. Two polls a
// moment apart would otherwise measure a sliver of time and report its noise;
// the second is given the first one's answer instead.
const cpuMinGap = time.Second

// cpuMaxGap is the longest: past it, the figure would be an average over
// minutes nobody asked about, and a fresh pair is taken.
const cpuMaxGap = 5 * time.Minute

// cpuColdSample is how long apart that fresh pair is.
const cpuColdSample = 500 * time.Millisecond

type cpuMeter struct {
	mu     sync.Mutex
	last   cpu.TimesStat
	lastAt time.Time
	pct    float64

	times func() (cpu.TimesStat, error)
	sleep func(time.Duration)
}

var hostCPU = &cpuMeter{times: totalTimes, sleep: time.Sleep}

func totalTimes() (cpu.TimesStat, error) {
	ts, err := cpu.Times(false)
	if err != nil || len(ts) == 0 {
		return cpu.TimesStat{}, err
	}
	return ts[0], nil
}

// read is the CPU use since the previous reading, in percent.
func (m *cpuMeter) read() (float64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	gap := now.Sub(m.lastAt)
	if !m.lastAt.IsZero() && gap < cpuMinGap {
		return m.pct, nil
	}
	prev := m.last
	if m.lastAt.IsZero() || gap > cpuMaxGap {
		var err error
		if prev, err = m.times(); err != nil {
			return 0, err
		}
		m.sleep(cpuColdSample)
		now = time.Now()
	}
	cur, err := m.times()
	if err != nil {
		return 0, err
	}
	m.pct = busyPercent(prev, cur)
	m.last, m.lastAt = cur, now
	return m.pct, nil
}

// busyPercent is gopsutil's own formula for the share of the time between two
// readings the CPUs spent on anything but waiting.
func busyPercent(t1, t2 cpu.TimesStat) float64 {
	all1, busy1 := allBusy(t1)
	all2, busy2 := allBusy(t2)
	if busy2 <= busy1 {
		return 0
	}
	if all2 <= all1 {
		return 100
	}
	return math.Min(100, math.Max(0, (busy2-busy1)/(all2-all1)*100))
}

func allBusy(t cpu.TimesStat) (all, busy float64) {
	all = t.User + t.System + t.Idle + t.Nice + t.Iowait + t.Irq + t.Softirq + t.Steal
	// Guest time is already counted in user time on Linux, and only there.
	if runtime.GOOS != "linux" {
		all += t.Guest + t.GuestNice
	}
	return all, all - t.Idle - t.Iowait
}
