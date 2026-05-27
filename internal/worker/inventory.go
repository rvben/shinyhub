package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/worker/api"
)

// containerLister is the subset of the worker DockerRuntime the inventory and
// re-adoption paths need. *process.DockerRuntime satisfies it.
type containerLister interface {
	ListByLabel(filter string) ([]process.ContainerInfo, error)
}

// handleInventory returns one item per managed container on this worker. The
// URL is the data-plane tunnel URL for the container's current token, or empty
// when no token is registered (the control plane then treats it as not routable).
func (s *replicaServer) handleInventory(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	lister, ok := s.runtime.(containerLister)
	if !ok {
		// Runtime cannot enumerate containers; report an empty inventory.
		_ = json.NewEncoder(w).Encode([]api.InventoryItem{})
		return
	}
	containers, err := lister.ListByLabel(`{"label":["shinyhub.managed=true"]}`)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	items := make([]api.InventoryItem, 0, len(containers))
	for _, c := range containers {
		s.mu.RLock()
		rec, tracked := s.byContainer[c.ID]
		s.mu.RUnlock()
		url := ""
		if tracked {
			url = fmt.Sprintf("https://%s/v1/data/%s", s.advertise, rec.token)
		}
		items = append(items, api.InventoryItem{
			ContainerID: c.ID,
			Labels:      c.Labels,
			Running:     true,
			URL:         url,
		})
	}
	_ = json.NewEncoder(w).Encode(items)
}

// Inventory aggregates the managed-container inventory of every up worker on the
// tier. Distinct-address workers coexist, so a replica can live on any of them;
// recovery reconciles each replica row against this combined inventory. It is
// best-effort: a per-worker dial or decode failure is skipped so the other
// workers' replicas still recover. It returns an error when there is no up
// worker, or when every up worker failed (so the caller does not mistake a
// total outage for an empty fleet and tear down healthy routes). When some
// workers succeed and others fail it returns the partial items alongside a
// *process.PartialInventoryError naming the unreachable workers, so recovery
// can avoid reconciling a replica owned by an unreachable worker as dead.
func (r *remoteRuntime) Inventory(ctx context.Context) ([]process.InventoryItem, error) {
	workers := r.lookup.WorkersForTier(r.tier)
	if len(workers) == 0 {
		return nil, fmt.Errorf("tier %q: %w", r.tier, process.ErrNoLiveWorker)
	}
	var items []process.InventoryItem
	var errs []error
	var failed []string
	for _, w := range workers {
		wi, err := r.inventoryFromWorker(ctx, w)
		if err != nil {
			errs = append(errs, fmt.Errorf("worker %s: %w", w.NodeID, err))
			failed = append(failed, w.NodeID)
			continue
		}
		items = append(items, wi...)
	}
	if len(errs) == len(workers) {
		return nil, errors.Join(errs...)
	}
	if len(errs) > 0 {
		for _, err := range errs {
			slog.Error("inventory: worker query failed", "tier", r.tier, "err", err)
		}
		return items, &process.PartialInventoryError{Workers: failed}
	}
	return items, nil
}

// inventoryFromWorker queries one worker's managed-container inventory.
func (r *remoteRuntime) inventoryFromWorker(ctx context.Context, w db.Worker) ([]process.InventoryItem, error) {
	client, base, err := r.dialer.DialWorker(w)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/inventory", nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("worker inventory returned %d", resp.StatusCode)
	}
	var wire []api.InventoryItem
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return nil, err
	}
	items := make([]process.InventoryItem, 0, len(wire))
	for _, it := range wire {
		items = append(items, process.InventoryItem{
			ContainerID: it.ContainerID,
			Labels:      it.Labels,
			Running:     it.Running,
			URL:         it.URL,
			WorkerID:    w.NodeID,
		})
	}
	return items, nil
}
