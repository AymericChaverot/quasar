// Package vps reports host-level CPU, memory and disk usage via gopsutil.
// Inside the dashboard container, HOST_PROC must point at the host's /proc
// (mounted read-only) and HOST_ROOT at the host's / for disk figures.
package vps

import (
	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
)

type Stats struct {
	CPUPercent  float64
	MemPercent  float64
	MemUsedGB   float64
	MemTotalGB  float64
	DiskPercent float64
	DiskUsedGB  float64
	DiskTotalGB float64

	// CPUs is how many logical CPUs the load is shared between.
	CPUs int

	// Load1 is the one-minute load average: how many processes wanted a CPU,
	// which says what a percentage cannot — whether the machine is queueing.
	Load1 float64
	// SwapUsedGB and SwapTotalGB are zero on the many VPSes that have none.
	SwapUsedGB  float64
	SwapTotalGB float64
}

// Collect reads CPU use since the previous reading (see cpu.go) and current
// memory, disk, load and swap usage.
func Collect(hostRoot string) (Stats, error) {
	var s Stats

	if pct, err := hostCPU.read(); err == nil {
		s.CPUPercent = pct
	}

	if vm, err := mem.VirtualMemory(); err == nil {
		s.MemPercent = vm.UsedPercent
		s.MemUsedGB = float64(vm.Used) / (1 << 30)
		s.MemTotalGB = float64(vm.Total) / (1 << 30)
	}

	if du, err := disk.Usage(hostRoot); err == nil {
		s.DiskPercent = du.UsedPercent
		s.DiskUsedGB = float64(du.Used) / (1 << 30)
		s.DiskTotalGB = float64(du.Total) / (1 << 30)
	}

	if n, err := cpu.Counts(true); err == nil {
		s.CPUs = n
	}

	if avg, err := load.Avg(); err == nil {
		s.Load1 = avg.Load1
	}

	if sw, err := mem.SwapMemory(); err == nil {
		s.SwapUsedGB = float64(sw.Used) / (1 << 30)
		s.SwapTotalGB = float64(sw.Total) / (1 << 30)
	}

	return s, nil
}

// Capacity is what the machine has to share out: its logical CPUs and its
// memory in megabytes. What an application uses is drawn against it. Cheap
// enough to read on every poll, and unlike Collect it takes no CPU reading,
// so it does not disturb the one the server's cards are measured against.
func Capacity() (cpus int, memMB float64) {
	if n, err := cpu.Counts(true); err == nil {
		cpus = n
	}
	if vm, err := mem.VirtualMemory(); err == nil {
		memMB = float64(vm.Total) / (1 << 20)
	}
	return cpus, memMB
}
