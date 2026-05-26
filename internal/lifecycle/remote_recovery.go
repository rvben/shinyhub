package lifecycle

import (
	"log/slog"
	"strconv"

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
