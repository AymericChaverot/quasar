package vps

import (
	"fmt"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v3/host"
)

// HostInfo describes the machine Quasar runs on. Inside the dashboard
// container, HOST_PROC/HOST_ETC must point at the host mounts so the values
// describe the server, not the Alpine container.
type HostInfo struct {
	OS     string // e.g. "Ubuntu 24.04"
	Kernel string
	Arch   string
	Uptime string
}

func CollectHost() HostInfo {
	out := HostInfo{OS: "unknown", Kernel: "unknown", Arch: "unknown"}
	info, err := host.Info()
	if err != nil {
		return out
	}
	os := strings.TrimSpace(info.Platform + " " + info.PlatformVersion)
	if os != "" {
		out.OS = os
	}
	if info.KernelVersion != "" {
		out.Kernel = info.KernelVersion
	}
	if info.KernelArch != "" {
		out.Arch = info.KernelArch
	}
	if info.Uptime > 0 {
		d := time.Duration(info.Uptime) * time.Second
		out.Uptime = fmt.Sprintf("%dd %dh", int(d.Hours())/24, int(d.Hours())%24)
	}
	return out
}
