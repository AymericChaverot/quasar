package vps

import (
	"fmt"
	"strings"

	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/mem"
)

// Hardware is what the machine has, as opposed to what it is currently using:
// the figures a Stats reading is a percentage of. Nothing here changes while
// the server is up, so it is read per page render rather than sampled.
//
// Every field can legitimately come back empty. A container without the host
// mounts, an ARM VPS whose /proc/cpuinfo carries no clock, or a hypervisor that
// hides the model name each leave a hole, and a hole is shown as such rather
// than as a zero.
type Hardware struct {
	CPUModel    string  // e.g. "Intel Xeon E5-2686 v4"
	CPUCores    int     // physical cores
	CPUThreads  int     // logical CPUs — what the scheduler actually hands out
	CPUGHz      float64 // nominal clock, not the current frequency
	MemTotalGB  float64
	SwapTotalGB float64
	DiskTotalGB float64
}

// CollectHardware reads the machine's fixed capacity. hostRoot is where the
// host filesystem is mounted, the same path the disk figures in Collect use.
func CollectHardware(hostRoot string) Hardware {
	var h Hardware

	if n, err := cpu.Counts(true); err == nil {
		h.CPUThreads = n
	}
	if n, err := cpu.Counts(false); err == nil {
		h.CPUCores = n
	}
	// One entry per logical CPU on Linux, all describing the same part on the
	// single-socket machines this runs on, so the first is enough for the name
	// and the clock.
	if infos, err := cpu.Info(); err == nil && len(infos) > 0 {
		h.CPUModel = strings.TrimSpace(infos[0].ModelName)
		if infos[0].Mhz > 0 {
			h.CPUGHz = infos[0].Mhz / 1000
		}
	}

	if vm, err := mem.VirtualMemory(); err == nil {
		h.MemTotalGB = float64(vm.Total) / (1 << 30)
	}
	if sw, err := mem.SwapMemory(); err == nil {
		h.SwapTotalGB = float64(sw.Total) / (1 << 30)
	}
	if du, err := disk.Usage(hostRoot); err == nil {
		h.DiskTotalGB = float64(du.Total) / (1 << 30)
	}
	return h
}

// CPULabel is the headline figure for the processor: how many CPUs the machine
// hands out and how fast each one nominally runs.
func (h Hardware) CPULabel() string {
	switch {
	case h.CPUThreads > 0 && h.CPUGHz > 0:
		return fmt.Sprintf("%d × %.2f GHz", h.CPUThreads, h.CPUGHz)
	case h.CPUThreads > 0:
		return fmt.Sprintf("%d vCPU", h.CPUThreads)
	case h.CPUGHz > 0:
		return fmt.Sprintf("%.2f GHz", h.CPUGHz)
	}
	return "unknown"
}

// CPUDetail names the part and, when it differs from the logical count, says
// how many physical cores are behind it: four threads on two cores do not
// perform like four cores, and the headline figure alone cannot tell them
// apart.
func (h Hardware) CPUDetail() string {
	var parts []string
	if h.CPUModel != "" {
		parts = append(parts, h.CPUModel)
	}
	if h.CPUCores > 0 && h.CPUCores != h.CPUThreads {
		parts = append(parts, fmt.Sprintf("%d physical %s", h.CPUCores, plural(h.CPUCores, "core", "cores")))
	}
	if len(parts) == 0 {
		return "model unavailable"
	}
	return strings.Join(parts, " · ")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
