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
	MaxSessionLifetime int
}

const baseWorkerOverheadMB = 150 // base RSS + shared libs + page cache headroom

func ValidateWorkerSettings(w WorkerSettings, clustered bool, effectiveMemMB, hostBudgetMB int) error {
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

// WorkerBudgetWarning returns a human-readable warning when elastic isolation
// (grouped/per_session) is configured with NO memory guard active: the static
// worst-case check is inert (it needs both host_budget_mb and a per-worker
// memory limit) and the runtime available-memory floor is off. In that state
// the kernel OOM killer is the only backstop, and it takes out a live worker
// with every session on it. Empty when guarded or when isolation is multiplex.
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
		"%s isolation has no memory guard: up to max_workers=%d workers may spawn with no host memory check, leaving the kernel OOM killer as the only backstop. Set server.host_budget_mb plus a per-app memory limit, or server.min_available_memory_mb.",
		w.Isolation, w.MaxWorkers)
}
