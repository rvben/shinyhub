package lifecycle

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/fargate"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
)

// matchInventoryItem returns the inventory item for the replica identified by
// slug + index + deploymentID owned by workerID, or nil when no live container
// matches. The deployment_id match is required so a container left behind by a
// superseded deployment (same slug+index, different deployment) is not
// re-adopted as current. An empty deploymentID (a legacy replica row predating
// deployment stamping) matches on slug+index alone. The workerID match is
// required because the tier inventory is aggregated across coexisting workers:
// a same-labeled container reported by another worker must not be adopted, or
// recovery would register that worker's URL while encoding the handle and
// selecting the transport for the owning worker. An empty workerID disables the
// owner check for callers that do not bind to a worker.
func matchInventoryItem(items []process.InventoryItem, slug string, index int, deploymentID, workerID string) *process.InventoryItem {
	idx := strconv.Itoa(index)
	for i := range items {
		l := items[i].Labels
		if l[process.LabelSlug] != slug || l[process.LabelReplicaIndex] != idx {
			continue
		}
		if deploymentID != "" && l[process.LabelDeploymentID] != deploymentID {
			continue
		}
		if workerID != "" && items[i].WorkerID != workerID {
			continue
		}
		return &items[i]
	}
	return nil
}

// recoverRemoteReplica re-adopts one replica on a remote tier by matching the
// tier's agent inventory. It returns true when the replica is adopted. A
// missing or stale container leaves the replica unadopted so the next deploy
// re-places it. The run handle is encoded as workerID + "/" + containerID to
// match the remote runtime's handle format, so later signal and stop calls
// resolve the owning worker and container.
func recoverRemoteReplica(
	store *db.Store, mgr *process.Manager, prx *proxy.Proxy,
	app *db.App, r *db.Replica, items []process.InventoryItem,
) bool {
	if r.Index >= app.Replicas {
		slog.Warn("recovery: remote replica index beyond pool; skipping",
			"slug", app.Slug, "idx", r.Index, "pool", app.Replicas)
		return false
	}
	if r.WorkerID == "" {
		slog.Warn("recovery: remote replica has no worker_id; skipping",
			"slug", app.Slug, "idx", r.Index)
		return false
	}
	depID := ""
	if r.DeploymentID != nil {
		depID = strconv.FormatInt(*r.DeploymentID, 10)
	}
	item := matchInventoryItem(items, app.Slug, r.Index, depID, r.WorkerID)

	// item == nil: no matching task in inventory; do not adopt.
	if item == nil {
		return false
	}

	// item.Running is false only when the task is STOPPED (see process.InventoryItem
	// godoc). A STOPPED task is genuinely gone; do not adopt.
	if !item.Running {
		return false
	}

	// Phase 1: task is running but not yet routable (no URL). Normal for ECS tasks
	// in PROVISIONING/PENDING state (both Fargate and EC2 launch types). Adopt into
	// the Manager to claim the slot and prevent a duplicate RunTask, but skip proxy
	// registration. A later recovery scan or the watcher completes full adoption
	// once the task acquires an IP.
	if item.URL == "" && fargate.IsECSManagedWorkerID(item.WorkerID) {
		mgr.Adopt(app.Slug, process.ProcessInfo{
			Slug:        app.Slug,
			Index:       r.Index,
			Status:      process.StatusRunning,
			Tier:        r.Tier,
			Provider:    r.Provider,
			EndpointURL: r.EndpointURL, // preserve DB value if present
			WorkerID:    r.WorkerID,
		}, process.RunHandle{ContainerID: r.WorkerID + "/" + item.ContainerID})
		slog.Info("recovery: partial-adopt ecs replica (no ip yet)",
			"slug", app.Slug, "idx", r.Index, "container", item.ContainerID,
			"launch_type", item.WorkerID)
		return true // slot is claimed; caller sets anyAlive=true
	}

	// item.URL == "" for a non-Fargate remote runtime: genuine worker error.
	if item.URL == "" {
		return false
	}

	// Phase 2: full adoption with proxy registration (task is routable).
	mgr.Adopt(app.Slug, process.ProcessInfo{
		Slug:        app.Slug,
		Index:       r.Index,
		Status:      process.StatusRunning,
		Tier:        r.Tier,
		Provider:    r.Provider,
		EndpointURL: item.URL,
		WorkerID:    r.WorkerID,
	}, process.RunHandle{ContainerID: r.WorkerID + "/" + item.ContainerID})
	if err := prx.RegisterReplica(app.Slug, r.Index, item.URL, mgr.TransportForWorker(r.Tier, r.WorkerID), derefInt64(r.DeploymentID)); err != nil {
		slog.Error("recovery: register remote proxy", "slug", app.Slug, "idx", r.Index, "err", err)
		return false
	}
	// Persist the URL the route was registered with so the stored endpoint_url
	// tracks the live route. The worker-loss path (down-sweep or admin revoke)
	// deregisters a slot only while the live route still equals the row's
	// endpoint_url; a stale or legacy-empty value would otherwise leave a dead
	// worker's route in place.
	if r.EndpointURL != item.URL {
		if err := store.UpdateReplicaEndpoint(app.ID, r.Index, item.URL); err != nil {
			slog.Error("recovery: persist remote endpoint", "slug", app.Slug, "idx", r.Index, "err", err)
		}
	}
	slog.Info("recovery: re-adopted remote replica", "slug", app.Slug, "idx", r.Index, "container", item.ContainerID)
	return true
}

// LoseWorkerReplicas transitions every running replica owned by nodeID to lost
// and removes it from routing via deregister (nil-safe). It is shared by the
// worker-down monitor (a worker whose heartbeat went stale) and the admin
// revoke path (a worker pulled administratively) so both evict in-flight user
// traffic from the worker identically. Replicas already in a terminal state are
// left untouched. A failure to resolve or update one replica is logged and
// skipped so the others still drain.
//
// deregister is passed the replica's expected routing target (its endpoint URL
// at the time of the snapshot) so it can drop the slot only while the live route
// still points at the lost replica: the deploy path registers a re-placed
// replica's route before persisting its row, so a stale loss pass must not pull
// a route a concurrent redeploy already re-pointed at a healthy backend.
//
// evict (nil-safe) drops the lost replica's entry from the process manager so a
// re-placement Start at the same slug+index is not rejected as already running.
// It is called only for slots this pass actually transitioned to lost, and is
// passed the lost worker's nodeID so the manager can skip an entry a concurrent
// redeploy already re-placed onto a healthy worker.
func LoseWorkerReplicas(store *db.Store, nodeID string, deregister func(slug string, index int, expectURL string), evict func(slug string, index int, workerID string)) error {
	reps, err := store.ListReplicasByWorker(nodeID)
	if err != nil {
		return err
	}
	for _, r := range reps {
		if r.Status != db.ReplicaStatusRunning {
			continue
		}
		app, err := store.GetAppByID(r.AppID)
		if err != nil {
			slog.Error("lose replica: resolve app", "app_id", r.AppID, "err", err)
			continue
		}
		// Guard the transition on the row still being running and still owned by
		// this worker: a concurrent redeploy may have re-placed this index onto a
		// healthy worker since the snapshot above, and we must not mark that
		// healthy replica lost nor pull its routing slot.
		changed, err := store.MarkReplicaLostIfOwnedBy(r.AppID, r.Index, nodeID)
		if err != nil {
			slog.Error("lose replica: mark lost", "slug", app.Slug, "idx", r.Index, "err", err)
			continue
		}
		if !changed {
			continue
		}
		if deregister != nil {
			deregister(app.Slug, r.Index, r.EndpointURL)
		}
		if evict != nil {
			evict(app.Slug, r.Index, nodeID)
		}
		slog.Warn("lose replica", "slug", app.Slug, "idx", r.Index, "node", nodeID)
	}
	return nil
}

// WorkerDownMonitor periodically marks stale workers down and transitions their
// running replicas to lost, removing them from proxy routing. It also reaps
// worker rows that have been down past a longer retention window so the table
// does not grow without bound. deregister is the proxy removal hook and forget
// drops a reaped worker from the routing index; both are injected so the monitor
// is unit-testable.
type WorkerDownMonitor struct {
	store      *db.Store
	timeout    time.Duration
	retention  time.Duration
	markDown   func(nodeID string) error
	deregister func(slug string, index int, expectURL string)
	evict      func(slug string, index int, workerID string)
	forget     func(nodeID string)
}

// NewWorkerDownMonitor builds a monitor that marks a worker down once its
// heartbeat is older than timeout and transitions that worker's running
// replicas to lost, calling deregister for each removed replica. markDown
// performs the down transition; wiring it to the registry keeps the in-memory
// routing index consistent with the store so a downed worker is excluded from
// routing without a control-plane restart. retention is the (much longer)
// window after which a still-dead, non-revoked worker row with no live replicas
// is reaped; forget drops the reaped node from the in-memory index. evict
// (nil-safe) drops each lost replica's entry from the process manager so a
// re-placement can start cleanly at the same slug+index.
func NewWorkerDownMonitor(store *db.Store, timeout, retention time.Duration, markDown func(nodeID string) error, deregister func(slug string, index int, expectURL string), evict func(slug string, index int, workerID string), forget func(nodeID string)) *WorkerDownMonitor {
	return &WorkerDownMonitor{store: store, timeout: timeout, retention: retention, markDown: markDown, deregister: deregister, evict: evict, forget: forget}
}

// Sweep performs one monitor pass as of now: every worker whose heartbeat
// predates now-timeout is marked down and each of its running replicas becomes
// lost and is deregistered from routing. Finally, worker rows that have been
// down past the retention window (and host no live replica) are reaped.
func (m *WorkerDownMonitor) Sweep(now time.Time) {
	stale, err := m.store.ListWorkersStale(now.Add(-m.timeout))
	if err != nil {
		slog.Error("worker monitor: list stale workers", "err", err)
		return
	}
	for _, w := range stale {
		if err := m.markDown(w.NodeID); err != nil {
			slog.Error("worker monitor: mark down", "node", w.NodeID, "err", err)
			continue
		}
		slog.Warn("worker monitor: worker down", "node", w.NodeID, "tier", w.Tier)
		if err := LoseWorkerReplicas(m.store, w.NodeID, m.deregister, m.evict); err != nil {
			slog.Error("worker monitor: lose replicas", "node", w.NodeID, "err", err)
			continue
		}
	}

	// Reap rows that have been down past the retention window. The store keeps
	// revoked rows (audit) and any worker still hosting a running/crashed
	// replica; here we only drop the reaped nodes from the in-memory index.
	reaped, err := m.store.DeleteStaleWorkers(now.Add(-m.retention))
	if err != nil {
		slog.Error("worker monitor: reap stale workers", "err", err)
		return
	}
	for _, nodeID := range reaped {
		if m.forget != nil {
			m.forget(nodeID)
		}
		slog.Info("worker monitor: reaped dead worker", "node", nodeID)
	}
}

// Run sweeps once per interval until ctx is cancelled.
func (m *WorkerDownMonitor) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			m.Sweep(now)
		}
	}
}
