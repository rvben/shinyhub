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
