package lifecycle

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
)

// matchInventoryItem returns the inventory item for the replica identified by
// slug + index + deploymentID, or nil when no live container matches. The
// deployment_id match is required so a container left behind by a superseded
// deployment (same slug+index, different deployment) is not re-adopted as
// current. An empty deploymentID (a legacy replica row predating deployment
// stamping) matches on slug+index alone.
func matchInventoryItem(items []process.InventoryItem, slug string, index int, deploymentID string) *process.InventoryItem {
	idx := strconv.Itoa(index)
	for i := range items {
		l := items[i].Labels
		if l["shinyhub.slug"] != slug || l["shinyhub.replica_index"] != idx {
			continue
		}
		if deploymentID != "" && l["shinyhub.deployment_id"] != deploymentID {
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
	mgr *process.Manager, prx *proxy.Proxy,
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
	item := matchInventoryItem(items, app.Slug, r.Index, depID)
	if item == nil || !item.Running || item.URL == "" {
		return false
	}
	mgr.Adopt(app.Slug, process.ProcessInfo{
		Slug:        app.Slug,
		Index:       r.Index,
		Status:      process.StatusRunning,
		Tier:        r.Tier,
		Provider:    r.Provider,
		EndpointURL: item.URL,
		WorkerID:    r.WorkerID,
	}, process.RunHandle{ContainerID: r.WorkerID + "/" + item.ContainerID})
	if err := prx.RegisterReplica(app.Slug, r.Index, item.URL, mgr.TransportForTier(r.Tier)); err != nil {
		slog.Error("recovery: register remote proxy", "slug", app.Slug, "idx", r.Index, "err", err)
		return false
	}
	slog.Info("recovery: re-adopted remote replica", "slug", app.Slug, "idx", r.Index, "container", item.ContainerID)
	return true
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
	deregister func(slug string, index int)
	forget     func(nodeID string)
}

// NewWorkerDownMonitor builds a monitor that marks a worker down once its
// heartbeat is older than timeout and transitions that worker's running
// replicas to lost, calling deregister for each removed replica. markDown
// performs the down transition; wiring it to the registry keeps the in-memory
// routing index consistent with the store so a downed worker is excluded from
// routing without a control-plane restart. retention is the (much longer)
// window after which a still-dead, non-revoked worker row with no live replicas
// is reaped; forget drops the reaped node from the in-memory index.
func NewWorkerDownMonitor(store *db.Store, timeout, retention time.Duration, markDown func(nodeID string) error, deregister func(slug string, index int), forget func(nodeID string)) *WorkerDownMonitor {
	return &WorkerDownMonitor{store: store, timeout: timeout, retention: retention, markDown: markDown, deregister: deregister, forget: forget}
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
		reps, err := m.store.ListReplicasByWorker(w.NodeID)
		if err != nil {
			slog.Error("worker monitor: list replicas", "node", w.NodeID, "err", err)
			continue
		}
		for _, r := range reps {
			if r.Status != db.ReplicaStatusRunning {
				continue
			}
			app, err := m.store.GetAppByID(r.AppID)
			if err != nil {
				slog.Error("worker monitor: resolve app", "app_id", r.AppID, "err", err)
				continue
			}
			if err := m.store.UpdateReplicaStatus(r.AppID, r.Index, db.ReplicaStatusLost); err != nil {
				slog.Error("worker monitor: mark replica lost", "slug", app.Slug, "idx", r.Index, "err", err)
				continue
			}
			if m.deregister != nil {
				m.deregister(app.Slug, r.Index)
			}
			slog.Warn("worker monitor: replica lost", "slug", app.Slug, "idx", r.Index, "node", w.NodeID)
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
