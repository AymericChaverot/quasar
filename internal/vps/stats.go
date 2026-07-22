// Package vps reports host-level CPU, memory and disk usage via gopsutil.
// Inside the dashboard container, HOST_PROC must point at the host's /proc
// (mounted read-only) and HOST_ROOT at the host's / for disk figures.
package vps

import (
	"time"

	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/mem"
)

type Stats struct {
	CPUPercent  float64
	MemPercent  float64
	MemUsedGB   float64
	MemTotalGB  float64
	DiskPercent float64
	DiskUsedGB  float64
	DiskTotalGB float64
}

// Collect samples CPU over 500ms and reads current memory/disk usage.
func Collect(hostRoot string) (Stats, error) {
	var s Stats

	if pcts, err := cpu.Percent(500*time.Millisecond, false); err == nil && len(pcts) > 0 {
		s.CPUPercent = pcts[0]
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

	return s, nil
}
