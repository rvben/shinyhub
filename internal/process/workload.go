package process

import (
	"strings"

	gops "github.com/shirou/gopsutil/v4/process"
)

// WorkloadObserver registers a native launch and returns its completion hook.
// Hooks must not perform network I/O or retain StartParams (which can contain
// secrets). The cgroup path is empty for launches without a dedicated cgroup.
// Set once before launching or adopting any workloads.
type WorkloadObserver func(StartParams, RunHandle, string) func()

func (r *NativeRuntime) SetWorkloadObserver(observer WorkloadObserver) {
	r.observer = observer
}

// ObserveRecoveredWorkload preserves the actual launch's OTel defaults and app
// overrides when its owner-readable environment is available. Other variables
// are never passed to the observer; platform defaults are the fallback.
func (r *NativeRuntime) ObserveRecoveredWorkload(p StartParams, handle RunHandle) {
	if r.observer == nil || handle.PID <= 0 {
		return
	}
	if proc, err := gops.NewProcess(int32(handle.PID)); err == nil {
		if env, err := proc.Environ(); err == nil {
			for _, kv := range env {
				if strings.HasPrefix(kv, "OTEL_RESOURCE_ATTRIBUTES=") || strings.HasPrefix(kv, "OTEL_SERVICE_NAME=") {
					p.Env = append(p.Env, kv)
				}
			}
		}
	}
	r.ObserveWorkload(p, handle)
}

// ObserveWorkload also registers a recovered process, after cgroup re-adoption.
func (r *NativeRuntime) ObserveWorkload(p StartParams, handle RunHandle) {
	if r.observer == nil || handle.PID <= 0 {
		return
	}
	r.mu.Lock()
	if _, observed := r.observationEnds[handle.PID]; observed {
		r.mu.Unlock()
		return
	}
	dir := r.appCgroups[handle.PID]
	// Reserve registration, but invoke observers outside the runtime lock.
	// finishObservation may remove the reservation if exit races registration.
	r.observationEnds[handle.PID] = nil
	r.mu.Unlock()
	end := r.observer(p, handle, dir)
	r.mu.Lock()
	_, reserved := r.observationEnds[handle.PID]
	if reserved {
		if end != nil {
			r.observationEnds[handle.PID] = end
		} else {
			delete(r.observationEnds, handle.PID)
		}
	}
	r.mu.Unlock()
	if !reserved && end != nil {
		end()
	}
}

func (r *NativeRuntime) finishObservation(pid int) {
	r.mu.Lock()
	end := r.observationEnds[pid]
	delete(r.observationEnds, pid)
	r.mu.Unlock()
	if end != nil {
		end()
	}
}
