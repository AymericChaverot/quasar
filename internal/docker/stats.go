package docker

import (
	"context"
	"encoding/json"
	"time"

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

// Stats reads a single (non-streamed) stats sample for the app. A compose app
// is the sum of its project's containers, since a stack's cost is all of it,
// not whichever service the daemon lists first.
func (c *Client) Stats(ctx context.Context, a *db.App) (ContainerStats, error) {
	if c.UsesCompose(a) {
		return c.composeStats(ctx, a.ID)
	}
	target, err := c.appContainer(ctx, a.ID)
	if err != nil {
		return ContainerStats{}, err
	}
	return c.StatsByName(ctx, target)
}

func (c *Client) composeStats(ctx context.Context, appID string) (ContainerStats, error) {
	list := c.composeContainers(ctx, appID)
	if len(list) == 0 {
		return ContainerStats{}, ErrNoContainer
	}
	var out ContainerStats
	var sampled int
	for _, ct := range list {
		if ct.State != "running" {
			continue // a stopped container has no stats stream to read
		}
		s, err := c.StatsByName(ctx, ct.ID)
		if err != nil {
			continue
		}
		sampled++
		out.CPUPercent += s.CPUPercent
		out.MemUsedMB += s.MemUsedMB
		// Containers of a project share the host's memory, so the limit is
		// whatever the most constrained one reports rather than a sum.
		if s.MemLimitMB > out.MemLimitMB {
			out.MemLimitMB = s.MemLimitMB
		}
	}
	if sampled == 0 {
		return ContainerStats{}, ErrNoContainer
	}
	if out.MemLimitMB > 0 {
		out.MemPercent = out.MemUsedMB / out.MemLimitMB * 100
	}
	return out, nil
}

// StatsByName reads a single (non-streamed) stats sample for any container by
// name — used for both app containers and read-only system containers.
//
// A one-shot sample is answered at once but carries no previous reading, so
// the daemon's own delta is empty and the formula would give the average since
// the container started rather than what it is doing now. The previous reading
// is kept here instead, per container, and the delta taken against it; only a
// container with no recent reading pays for the daemon's own two, about a
// second apart.
func (c *Client) StatsByName(ctx context.Context, name string) (ContainerStats, error) {
	s, err := c.readStats(ctx, name, true)
	if err != nil {
		return ContainerStats{}, err
	}
	cur := cpuSampleOf(s.CPUStats)
	prev, ok := c.swapCPUSample(s.ID, cur)
	if !ok {
		if full, err := c.readStats(ctx, name, false); err == nil {
			s = full
			cur = cpuSampleOf(s.CPUStats)
			c.swapCPUSample(s.ID, cur)
		}
		prev = cpuSampleOf(s.PreCPUStats)
	}

	out := ContainerStats{CPUPercent: cpuPercent(prev, cur)}
	memUsed := s.MemoryStats.Usage - s.MemoryStats.Stats["inactive_file"]
	out.MemUsedMB = float64(memUsed) / (1 << 20)
	out.MemLimitMB = float64(s.MemoryStats.Limit) / (1 << 20)
	if s.MemoryStats.Limit > 0 {
		out.MemPercent = float64(memUsed) / float64(s.MemoryStats.Limit) * 100
	}
	return out, nil
}

// readStats asks the daemon for one reading: at once when oneShot, or after
// the second of its two samples otherwise, which is what fills PreCPUStats.
func (c *Client) readStats(ctx context.Context, name string, oneShot bool) (container.StatsResponse, error) {
	var s container.StatsResponse
	var resp container.StatsResponseReader
	var err error
	if oneShot {
		resp, err = c.api.ContainerStatsOneShot(ctx, name)
	} else {
		resp, err = c.api.ContainerStats(ctx, name, false)
	}
	if err != nil {
		return s, err
	}
	defer resp.Body.Close()
	err = json.NewDecoder(resp.Body).Decode(&s)
	return s, err
}

// cpuSample is the part of a stats reading the CPU percentage is a delta of.
type cpuSample struct {
	total, system uint64
	cpus          float64
	at            time.Time
}

func cpuSampleOf(s container.CPUStats) cpuSample {
	cpus := float64(s.OnlineCPUs)
	if cpus == 0 {
		cpus = float64(len(s.CPUUsage.PercpuUsage))
	}
	return cpuSample{total: s.CPUUsage.TotalUsage, system: s.SystemUsage, cpus: cpus, at: time.Now()}
}

// cpuPercent is Docker's own formula: the container's share of the CPU time
// the whole system spent between two readings, scaled by the number of CPUs.
func cpuPercent(prev, cur cpuSample) float64 {
	if cur.total < prev.total || cur.system <= prev.system {
		return 0
	}
	return float64(cur.total-prev.total) / float64(cur.system-prev.system) * cur.cpus * 100
}

// cpuSampleTTL is how old a previous reading may be and still be compared
// against: past it, the figure would be an average over minutes nobody asked
// about.
const cpuSampleTTL = 5 * time.Minute

// swapCPUSample records cur as the container's latest reading and returns the
// one it replaces, when that one is recent enough to compare against.
func (c *Client) swapCPUSample(id string, cur cpuSample) (cpuSample, bool) {
	c.cpuMu.Lock()
	defer c.cpuMu.Unlock()
	if c.cpuSamples == nil {
		c.cpuSamples = map[string]cpuSample{}
	}
	prev, ok := c.cpuSamples[id]
	c.cpuSamples[id] = cur
	// Containers come and go; one not read for a while is forgotten.
	for k, v := range c.cpuSamples {
		if cur.at.Sub(v.at) > cpuSampleTTL {
			delete(c.cpuSamples, k)
		}
	}
	age := cur.at.Sub(prev.at)
	return prev, ok && age > 0 && age <= cpuSampleTTL && cur.system > prev.system
}
