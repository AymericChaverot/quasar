package docker

// Changing how much CPU and memory an application may use, without recreating
// the container running it.
//
// The limits are written into a container when it is created, which is why they
// used to be settable at that one moment only — on the form that creates the
// app, and never again. Docker can however retighten a container's cgroup in
// place, so a limit raised, lowered or imposed for the first time takes effect
// on the container already serving, with no downtime and nothing to redeploy.
//
// There is one direction it cannot go. The Engine reads a zero in an update as
// "leave this as it is", so lifting a limit back to unlimited cannot be said at
// all, and only a redeploy — which creates the container afresh — carries it
// out. That is why what is stored and what the container enforces are compared
// rather than assumed to agree: the panel has to be able to say a redeploy is
// still owed.

import (
	"context"
	"fmt"
	"strconv"

	"github.com/docker/docker/api/types/container"

	"quasar/internal/db"
)

// LimitsState is what the container serving an app actually enforces, and
// whether that is what the app is configured with.
type LimitsState struct {
	// Known is false when there is no container to read: a stack, whose limits
	// are its compose file's business, or an app that has never been deployed.
	Known      bool
	CPULimit   float64 // cores, 0 = unlimited
	MemLimitMB int64   // MB, 0 = unlimited
	// Pending is true when the container runs under limits other than the
	// stored ones, which only a redeploy can now reconcile.
	Pending bool
}

// Text names the limits the way the page does.
func (s LimitsState) Text() string { return LimitsText(s.CPULimit, s.MemLimitMB) }

// LimitsText renders a pair of limits as one phrase, "unlimited" for none.
func LimitsText(cpu float64, memMB int64) string {
	cpuText := strconv.FormatFloat(cpu, 'g', -1, 64) + " CPU"
	memText := strconv.FormatInt(memMB, 10) + " MB"
	switch {
	case cpu > 0 && memMB > 0:
		return cpuText + " · " + memText
	case cpu > 0:
		return cpuText
	case memMB > 0:
		return memText
	}
	return "unlimited"
}

// nanoCPUs and memBytes are the app's limits in the units the Engine takes.
func nanoCPUs(a *db.App) int64 { return int64(a.CPULimit * 1e9) }
func memBytes(a *db.App) int64 { return a.MemLimitMB << 20 }

// appResources is what a container is created with, and the single description
// of the app's limits that both the deploy and a live change work from.
func appResources(a *db.App) container.Resources {
	return container.Resources{NanoCPUs: nanoCPUs(a), Memory: memBytes(a)}
}

// limitsUpdate is the app's limits expressed as an in-place change to a running
// container. A field left at zero means "unchanged" to the Engine, so a limit
// being lifted is simply absent here — Limits is what then reports that the
// container is still holding the old one and a redeploy is owed.
func limitsUpdate(a *db.App) container.Resources {
	r := container.Resources{NanoCPUs: nanoCPUs(a)}
	if mem := memBytes(a); mem > 0 {
		r.Memory = mem
		// Swap has to move with the limit, or the Engine refuses a memory limit
		// that would fall below the swap total the container already carries.
		// Twice the limit is what creating a container with this memory and no
		// swap setting of its own produces, so changing a limit live and
		// redeploying leave the container in the same place.
		r.MemorySwap = 2 * mem
	}
	return r
}

// limitsApplied reports whether a container created or updated with these
// settings enforces exactly the limits the app is configured with.
func limitsApplied(a *db.App, host *container.HostConfig) bool {
	return host != nil && host.NanoCPUs == nanoCPUs(a) && host.Memory == memBytes(a)
}

// Limits reads what the app's container is really running under.
func (c *Client) Limits(ctx context.Context, a *db.App) LimitsState {
	// A stack's containers are described by its compose file, which Quasar does
	// not write resource limits into; there is nothing here to report on.
	if c.UsesCompose(a) {
		return LimitsState{}
	}
	id, err := c.appContainer(ctx, a.ID)
	if err != nil {
		return LimitsState{}
	}
	return c.limitsOf(ctx, a, id)
}

// limitsOf is Limits for a container already resolved.
func (c *Client) limitsOf(ctx context.Context, a *db.App, id string) LimitsState {
	info, err := c.api.ContainerInspect(ctx, id)
	if err != nil || info.HostConfig == nil {
		return LimitsState{}
	}
	return LimitsState{
		Known:      true,
		CPULimit:   float64(info.HostConfig.NanoCPUs) / 1e9,
		MemLimitMB: info.HostConfig.Memory >> 20,
		Pending:    !limitsApplied(a, info.HostConfig),
	}
}

// ApplyLimits pushes the app's stored limits onto the container serving it and
// reports what that container enforces afterwards. A stack, or an app with no
// container yet, is left alone: there the stored limits wait for a deploy.
//
// The error is the Engine refusing the change — more CPUs than the host has,
// a memory limit under its minimum — and is worth showing, but it is not the
// caller's failure: the limits are already stored either way, and a redeploy
// will still apply them.
func (c *Client) ApplyLimits(ctx context.Context, a *db.App) (LimitsState, error) {
	if c.UsesCompose(a) {
		return LimitsState{}, nil
	}
	id, err := c.appContainer(ctx, a.ID)
	if err != nil {
		return LimitsState{}, nil //nolint:nilerr // no container yet: the limits are stored, a redeploy applies them
	}
	update := limitsUpdate(a)
	if update.NanoCPUs != 0 || update.Memory != 0 {
		if _, err := c.api.ContainerUpdate(ctx, id, container.UpdateConfig{Resources: update}); err != nil {
			return c.limitsOf(ctx, a, id), fmt.Errorf("the running container refused the change: %w", err)
		}
	}
	return c.limitsOf(ctx, a, id), nil
}
