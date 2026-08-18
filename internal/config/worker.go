package config

import "fmt"

type WorkerIsolationMode string

const (
	IsolationMultiplex  WorkerIsolationMode = "multiplex"
	IsolationGrouped    WorkerIsolationMode = "grouped"
	IsolationPerSession WorkerIsolationMode = "per_session"
)

type WorkerSettings struct {
	Isolation          WorkerIsolationMode
	GroupedSize        int
	MaxWorkers         int
	WarmSpares         int
	MaxSessionLifetime int
}

const baseWorkerOverheadMB = 150 // base RSS + shared libs + page cache headroom

func ValidateWorkerSettings(w WorkerSettings, clustered bool, effectiveMemMB, hostBudgetMB int) error {
	// Warm-spare state is persisted even while multiplex ignores it, so enforce
	// its storage-safe range before the isolation early return. This lets an
	// operator preconfigure a future elastic switch without allowing a DB
	// constraint failure to surface as a 500.
	if w.WarmSpares < 0 || w.WarmSpares > 1000 {
		return fmt.Errorf("worker.warm_spares must be between 0 and 1000")
	}
	switch w.Isolation {
	case "", IsolationMultiplex:
		return nil // multiplex ignores the other knobs
	case IsolationGrouped, IsolationPerSession:
		// fall through to shared checks
	default:
		return fmt.Errorf("worker.isolation %q is invalid; want multiplex, grouped, or per_session", w.Isolation)
	}
	if clustered {
		return fmt.Errorf("worker.isolation %q requires a single-node deployment; it is not supported with a Postgres DSN (revert to multiplex or run single-node)", w.Isolation)
	}
	if w.Isolation == IsolationGrouped && w.GroupedSize < 1 {
		return fmt.Errorf("worker.grouped_size must be >= 1 when isolation is grouped")
	}
	if w.MaxWorkers < 1 {
		return fmt.Errorf("worker.max_workers must be >= 1 for %s", w.Isolation)
	}
	if w.WarmSpares > w.MaxWorkers {
		return fmt.Errorf("worker.warm_spares=%d must be <= worker.max_workers=%d", w.WarmSpares, w.MaxWorkers)
	}
	if w.MaxSessionLifetime < 0 {
		return fmt.Errorf("worker.max_session_lifetime must be >= 0 (0 = unlimited)")
	}
	if effectiveMemMB > 0 && hostBudgetMB > 0 {
		worst := w.MaxWorkers * (effectiveMemMB + baseWorkerOverheadMB)
		if worst > hostBudgetMB {
			return fmt.Errorf("worker.max_workers=%d x (%dMB limit + %dMB overhead) = %dMB exceeds the host budget of %dMB",
				w.MaxWorkers, effectiveMemMB, baseWorkerOverheadMB, worst, hostBudgetMB)
		}
	}
	return nil
}

// MinWarmReplicasInertWarning returns a warning when a keep-warm floor cannot
// take effect: min_warm_replicas keeps multiplex replicas running through idle
// hibernation, but an elastic pool (grouped / per_session) has no standing
// replicas at all. Its workers boot on demand and the app reports idle, which
// is healthy, with none running, so the floor is accepted (it applies again the
// moment isolation returns to multiplex) but does nothing now. The message
// names the knob that does pre-boot elastic workers. Empty when the floor is
// unset or the isolation is multiplex. isolation must already be resolved
// against the fleet default.
func MinWarmReplicasInertWarning(minWarm int, isolation WorkerIsolationMode) string {
	if minWarm <= 0 {
		return ""
	}
	switch isolation {
	case IsolationGrouped, IsolationPerSession:
	default:
		return ""
	}
	return fmt.Sprintf(
		"min_warm_replicas=%d has no effect under worker.isolation=%s: elastic pools boot workers on demand and report idle (healthy) with none running; set worker.warm_spares to keep workers pre-booted",
		minWarm, isolation)
}

// WorkerBudgetWarning returns a human-readable warning when elastic isolation
// (grouped/per_session) is configured with NO memory guard active: the static
// worst-case check is inert (it needs both host_budget_mb and a per-worker
// memory limit) and the runtime available-memory floor is off. Because the
// floor is on by default, this state is only reachable when the operator
// explicitly disabled it (min_available_memory_mb: 0) without arming the
// static guard - the warning reminds them the kernel OOM killer is then the
// only backstop, and it takes out a live worker with every session on it.
// Empty when guarded or when isolation is multiplex.
func WorkerBudgetWarning(w WorkerSettings, effectiveMemMB, hostBudgetMB, minAvailableMB int) string {
	switch w.Isolation {
	case IsolationGrouped, IsolationPerSession:
	default:
		return ""
	}
	staticActive := effectiveMemMB > 0 && hostBudgetMB > 0
	if staticActive || minAvailableMB > 0 {
		return ""
	}
	return fmt.Sprintf(
		"%s isolation has no memory guard: up to max_workers=%d workers may spawn with no host memory check, leaving the kernel OOM killer as the only backstop. Re-enable server.min_available_memory_mb (on by default; explicitly disabled here), or set server.host_budget_mb plus a per-app memory limit.",
		w.Isolation, w.MaxWorkers)
}
