// Package vps reports host-level CPU, memory and disk usage via gopsutil.
// Inside the dashboard container, HOST_PROC must point at the host's /proc
// (mounted read-only) and HOST_ROOT at the host's / for disk figures.
package vps

import (
	"github.com/shirou/gopsutil/v4/disk"
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
}

// Collect reads CPU use since the previous reading (see cpu.go) and current
// memory/disk usage.
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

	return s, nil
}
