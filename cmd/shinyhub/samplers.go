package main

import (
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/process"
)

// hostSampler samples PID-backed replicas (native) via gopsutil and reports
// PID-less replicas (docker/remote/fargate handles, whose RunHandle carries a
// ContainerID rather than a PID) as unmeasured without error: the zero Stats it
// returns carries a nil CPU rate, so they render as "not available" rather than
// as an idle app. Returning no error for the PID-less case is deliberate: the
// status endpoint treats a sampler error as a dead replica, so a running replica
// on a container/fargate tier must not be probed by PID (PID 0) and misreported
// as stopped.
type hostSampler struct{ gops process.GopsutilSampler }

func (h *hostSampler) Sample(handle process.RunHandle) (process.Stats, error) {
	if handle.PID == 0 {
		return process.Stats{}, nil
	}
	return h.gops.Sample(handle)
}

// replicaSamplers returns the sampler the API reads replica usage through
// and a separate one for the metrics-history collector, so the collector's
// CPU deltas are independent of on-demand API reads.
//
// A docker default tier samples container stats through the tier's Runtime.
// Otherwise the API uses hostSampler, which tolerates PID-less replicas, and
// history samples host PIDs directly. The default tier's resolved runtime
// decides rather than the legacy cfg.Runtime.Mode: a config that declares
// tiers:[{runtime:docker}] without runtime.mode still samples containers.
func replicaSamplers(cfg *config.Config, mgr *process.Manager) (api, history process.Sampler) {
	tier := defaultTierConfig(cfg)
	if tier.Runtime == "docker" {
		rt := mgr.RuntimeForTier(tier.Name)
		return &process.RuntimeSampler{Runtime: rt}, &process.RuntimeSampler{Runtime: rt}
	}
	return &hostSampler{}, &process.GopsutilSampler{}
}
