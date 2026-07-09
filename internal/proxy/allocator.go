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
	decisionRoute    decisionKind = iota // route to an existing ready worker
	decisionBind                         // bind to a booting worker; the client waits on the loading page
	decisionAllocate                     // spin up a new worker slot
	decisionReject                       // at capacity, deny the request
)

type decision struct {
	kind   decisionKind
	slotID int // valid when kind == decisionRoute or decisionBind
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
	// Pack: most-loaded worker still under cap. Ready workers are preferred
	// (the client is served immediately); a booting worker under cap is the
	// next-best placement - the client cannot be routed there (its rp is nil)
	// but it can be BOUND there and wait on the loading page. Binding to
	// booting workers is what keeps a burst of cold clients packed to
	// grouped_size per worker: without it every concurrent arrival would
	// reserve its own slot and the pool would shed at max_workers clients
	// instead of the configured max_workers x grouped_size ceiling.
	bestReady, bestReadyClients := -1, -1
	bestBooting, bestBootingClients := -1, -1
	active := 0
	for _, wkr := range workers {
		if wkr.status == workerDraining {
			continue
		}
		active++ // booting slots count toward maxWorkers
		if wkr.assignedClients >= cap {
			continue
		}
		switch wkr.status {
		case workerRunning:
			if wkr.assignedClients > bestReadyClients {
				bestReady, bestReadyClients = wkr.slotID, wkr.assignedClients
			}
		case workerBooting:
			if wkr.assignedClients > bestBootingClients {
				bestBooting, bestBootingClients = wkr.slotID, wkr.assignedClients
			}
		}
	}
	if bestReady >= 0 {
		return decision{kind: decisionRoute, slotID: bestReady}
	}
	if bestBooting >= 0 {
		return decision{kind: decisionBind, slotID: bestBooting}
	}
	if active < maxWorkers {
		return decision{kind: decisionAllocate}
	}
	return decision{kind: decisionReject}
}
