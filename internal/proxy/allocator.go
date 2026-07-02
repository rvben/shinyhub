package proxy

import "github.com/rvben/shinyhub/internal/config"

type workerStatus int

const (
	workerRunning  workerStatus = iota
	workerBooting               // slot reserved, reverse-proxy not yet installed
	workerDraining              // draining existing clients, no new routing
)

type workerState struct {
	slotID          int
	assignedClients int
	status          workerStatus
}

type decisionKind int

const (
	decisionRoute    decisionKind = iota // route to an existing worker
	decisionAllocate                     // spin up a new worker slot
	decisionReject                       // at capacity, deny the request
)

type decision struct {
	kind   decisionKind
	slotID int // valid when kind == decisionRoute
}

func perWorkerCap(mode config.WorkerIsolationMode, groupedSize int) int {
	if mode == config.IsolationPerSession {
		return 1
	}
	return groupedSize // grouped
}

// decide is pure: no I/O, no locks. Callers hold the pool lock and pass a
// snapshot. pinnedSlot is -1 when the request has no valid routing pin.
func decide(workers []workerState, mode config.WorkerIsolationMode, groupedSize, maxWorkers, pinnedSlot int) decision {
	if pinnedSlot >= 0 {
		for _, wkr := range workers {
			if wkr.slotID == pinnedSlot && wkr.status != workerDraining {
				return decision{kind: decisionRoute, slotID: pinnedSlot}
			}
		}
	}
	cap := perWorkerCap(mode, groupedSize)
	// Pack: most-loaded worker still under cap.
	best, bestClients, active := -1, -1, 0
	for _, wkr := range workers {
		if wkr.status == workerDraining {
			continue
		}
		active++ // booting slots count toward maxWorkers
		if wkr.status == workerBooting {
			continue // never route a new client to a not-yet-ready worker (its rp is nil)
		}
		if wkr.assignedClients < cap && wkr.assignedClients > bestClients {
			best, bestClients = wkr.slotID, wkr.assignedClients
		}
	}
	if best >= 0 {
		return decision{kind: decisionRoute, slotID: best}
	}
	if active < maxWorkers {
		return decision{kind: decisionAllocate}
	}
	return decision{kind: decisionReject}
}
