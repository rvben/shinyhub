package api

import (
	"context"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/process"
)

func (s *Server) applyLiveResources(ctx context.Context, app *db.App, oldMemory, newMemory, oldCPU, newCPU *int, retryAll bool) error {
	mem, cpu := s.cfg.Runtime.DefaultResourcesForApp(app)
	oldM, newM := deploy.ResolveMemoryLimitMB(oldMemory, mem), deploy.ResolveMemoryLimitMB(newMemory, mem)
	oldC, newC := deploy.ResolveCPUQuotaPercent(oldCPU, cpu), deploy.ResolveCPUQuotaPercent(newCPU, cpu)
	var limits process.ResourceLimits
	if oldM != newM {
		// Setting memory.max below the current usage may trigger an OOM kill.
		// Give these changes the same explicit drain as immutable runtimes.
		if newM > 0 && (oldM == 0 || newM < oldM) {
			return process.ErrLiveResourcesUnsupported
		}
		limits.MemoryLimitMB = &newM
	}
	if oldC != newC {
		limits.CPUQuotaPercent = &newC
	}
	if retryAll {
		limits.MemoryLimitMB = &newM
		limits.CPUQuotaPercent = &newC
	}
	if limits.MemoryLimitMB == nil && limits.CPUQuotaPercent == nil {
		return nil
	}
	return s.manager.UpdateResources(ctx, app.Slug, limits)
}
