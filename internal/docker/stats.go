package docker

import (
	"context"
	"encoding/json"

	"github.com/docker/docker/api/types/container"

	"quasar/internal/db"
)

// ContainerStats is a one-shot CPU/memory snapshot for an app's container.
type ContainerStats struct {
	CPUPercent float64
	MemUsedMB  float64
	MemLimitMB float64
	MemPercent float64
}

// Stats reads a single (non-streamed) stats sample for the app container.
func (c *Client) Stats(ctx context.Context, a *db.App) (ContainerStats, error) {
	return c.StatsByName(ctx, ContainerName(a.ID))
}

// StatsByName reads a single (non-streamed) stats sample for any container by
// name — used for both app containers and read-only system containers.
func (c *Client) StatsByName(ctx context.Context, name string) (ContainerStats, error) {
	var out ContainerStats

	resp, err := c.api.ContainerStatsOneShot(ctx, name)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()

	var s container.StatsResponse
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return out, err
	}

	// Standard Docker CPU% formula: delta of container CPU over delta of
	// system CPU, scaled by the number of online CPUs.
	cpuDelta := float64(s.CPUStats.CPUUsage.TotalUsage - s.PreCPUStats.CPUUsage.TotalUsage)
	sysDelta := float64(s.CPUStats.SystemUsage - s.PreCPUStats.SystemUsage)
	if sysDelta > 0 && cpuDelta > 0 {
		cpus := float64(s.CPUStats.OnlineCPUs)
		if cpus == 0 {
			cpus = float64(len(s.CPUStats.CPUUsage.PercpuUsage))
		}
		out.CPUPercent = cpuDelta / sysDelta * cpus * 100
	}

	memUsed := s.MemoryStats.Usage - s.MemoryStats.Stats["inactive_file"]
	out.MemUsedMB = float64(memUsed) / (1 << 20)
	out.MemLimitMB = float64(s.MemoryStats.Limit) / (1 << 20)
	if s.MemoryStats.Limit > 0 {
		out.MemPercent = float64(memUsed) / float64(s.MemoryStats.Limit) * 100
	}
	return out, nil
}
