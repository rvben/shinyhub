package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

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

// Inventory asks the tier's live worker for its managed-container inventory.
func (r *remoteRuntime) Inventory(ctx context.Context) ([]process.InventoryItem, error) {
	w, err := r.liveWorker()
	if err != nil {
		return nil, err
	}
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
		})
	}
	return items, nil
}
