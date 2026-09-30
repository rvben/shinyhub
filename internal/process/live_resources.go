package process

import (
	"context"
	"errors"
	"fmt"
)

var ErrLiveResourcesUnsupported = errors.New("runtime requires replacement to change resource limits")

// ResourceLimits contains only changed effective values. Nil leaves a limit
// untouched; zero removes it. Callers must drain before reducing memory.
type ResourceLimits struct {
	MemoryLimitMB   *int
	CPUQuotaPercent *int
}

type ResourceUpdater interface {
	UpdateResources(context.Context, RunHandle, ResourceLimits) error
}

// UpdateResources preserves process identities and covers elastic workers as
// well as the selected generation. Lifecycle callers serialize this operation
// against start, stop and generation activation with the app operation lock.
func (m *Manager) UpdateResources(ctx context.Context, slug string, limits ResourceLimits) error {
	type target struct {
		updater ResourceUpdater
		handle  RunHandle
		index   int
	}
	m.mu.Lock()
	var targets []target
	for _, e := range m.entries[m.activePoolKeyLocked(slug)] {
		if e == nil || (e.info.Status != StatusRunning && e.info.Status != StatusSuspended) {
			continue
		}
		updater, ok := m.runtimeFor(e.tier).(ResourceUpdater)
		if !ok {
			m.mu.Unlock()
			return ErrLiveResourcesUnsupported
		}
		targets = append(targets, target{updater, e.handle, e.info.Index})
	}
	m.mu.Unlock()
	if len(targets) == 0 {
		return ErrLiveResourcesUnsupported
	}
	for _, t := range targets {
		if err := t.updater.UpdateResources(ctx, t.handle, limits); err != nil {
			return fmt.Errorf("replica %d: %w", t.index, err)
		}
	}
	return nil
}

func (r *NativeRuntime) UpdateResources(_ context.Context, handle RunHandle, limits ResourceLimits) error {
	r.ensureCgroupBase()
	r.mu.Lock()
	defer r.mu.Unlock()
	memory := limits.MemoryLimitMB != nil && r.limitsMemoryEnforced
	cpu := limits.CPUQuotaPercent != nil && r.limitsCPUEnforced
	// The API already reports missing enforcement. Restarting an uncapped
	// process cannot enable a controller the host has not delegated.
	if !memory && !cpu {
		return nil
	}
	dir := r.appCgroups[handle.PID]
	if dir == "" {
		return ErrLiveResourcesUnsupported
	}
	if memory {
		if err := setCgroupMemoryMax(dir, *limits.MemoryLimitMB); err != nil {
			return err
		}
	}
	if cpu {
		if err := setCgroupCPUMax(dir, *limits.CPUQuotaPercent); err != nil {
			return err
		}
	}
	return nil
}

func (r *DockerRuntime) UpdateResources(ctx context.Context, handle RunHandle, limits ResourceLimits) error {
	if handle.ContainerID == "" {
		return ErrLiveResourcesUnsupported
	}
	// Zero cannot remove an existing Docker limit, but a retry of an
	// already-unlimited sibling must not trigger a needless replacement.
	var state struct {
		HostConfig struct{ Memory, MemorySwap, NanoCpus int64 }
	}
	inspect := limits.MemoryLimitMB != nil || (limits.CPUQuotaPercent != nil && *limits.CPUQuotaPercent <= 0)
	if inspect {
		if err := r.client.get("/containers/"+handle.ContainerID+"/json", &state); err != nil {
			return err
		}
	}
	if (limits.MemoryLimitMB != nil && *limits.MemoryLimitMB <= 0 && state.HostConfig.Memory != 0) ||
		(limits.CPUQuotaPercent != nil && *limits.CPUQuotaPercent <= 0 && state.HostConfig.NanoCpus != 0) {
		return ErrLiveResourcesUnsupported
	}
	body := make(map[string]int64)
	if limits.MemoryLimitMB != nil && *limits.MemoryLimitMB > 0 {
		memory := int64(*limits.MemoryLimitMB) * 1024 * 1024
		body["Memory"] = memory
		// Preserve the existing swap allowance as memory grows.
		if state.HostConfig.MemorySwap > 0 {
			body["MemorySwap"] = memory + max(0, state.HostConfig.MemorySwap-state.HostConfig.Memory)
		}
	}
	if limits.CPUQuotaPercent != nil && *limits.CPUQuotaPercent > 0 {
		body["NanoCpus"] = int64(*limits.CPUQuotaPercent) * 10_000_000
	}
	if len(body) == 0 {
		return nil
	}
	var result struct{ Warnings []string }
	if err := r.client.postContext(ctx, "/containers/"+handle.ContainerID+"/update", body, &result); err != nil {
		return err
	}
	if len(result.Warnings) > 0 {
		return fmt.Errorf("resource update warnings: %v", result.Warnings)
	}
	return nil
}
