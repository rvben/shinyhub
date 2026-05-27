package api

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/lifecycle"
)

// workerResponse is the admin-facing view of a joined worker.
type workerResponse struct {
	NodeID        string `json:"node_id"`
	Name          string `json:"name"`
	Tier          string `json:"tier"`
	AdvertiseAddr string `json:"advertise_addr"`
	Status        string `json:"status"`
	Version       string `json:"version"`
	LastHeartbeat string `json:"last_heartbeat"`
	Revoked       bool   `json:"revoked"`
	RevokedAt     string `json:"revoked_at,omitempty"`
}

func toWorkerResponse(w db.Worker) workerResponse {
	return workerResponse{
		NodeID:        w.NodeID,
		Name:          w.Name,
		Tier:          w.Tier,
		AdvertiseAddr: w.AdvertiseAddr,
		Status:        w.Status,
		Version:       w.Version,
		LastHeartbeat: w.LastHeartbeat,
		Revoked:       w.Revoked(),
		RevokedAt:     w.RevokedAt,
	}
}

// handleListWorkers returns the joined-worker fleet, including down and revoked
// nodes, ordered by node id. Admin only. Returns an empty list when worker
// hosting is disabled. It reads from the store, the authoritative source for
// per-worker metadata such as last_heartbeat: the in-memory registry index
// tracks routing status but is not refreshed with the heartbeat timestamp the
// store writes server-side, so listing from it would report stale liveness.
func (s *Server) handleListWorkers(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	out := []workerResponse{}
	if s.workerReg != nil {
		workers, err := s.store.ListWorkers()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}
		for _, wk := range workers {
			out = append(out, toWorkerResponse(*wk))
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleRevokeWorker administratively revokes a worker: its certificate is
// rejected by the worker API and excluded from control->worker dials
// immediately, independent of its short cert TTL. Admin only. Returns 404 for
// an unknown node (or when worker hosting is disabled).
func (s *Server) handleRevokeWorker(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	nodeID := chi.URLParam(r, "node_id")
	if s.workerReg == nil {
		writeError(w, http.StatusNotFound, "worker not found")
		return
	}
	if err := s.workerReg.Revoke(nodeID); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "worker not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	// Revocation marks the worker down, but the down-timeout sweep skips workers
	// that are already down, so its running replicas would otherwise keep serving
	// user traffic. Evict them here: transition each to lost and drop its proxy
	// route, mirroring how the monitor handles a worker that went stale.
	var deregister func(slug string, index int, expectURL string)
	if s.proxy != nil {
		deregister = func(slug string, index int, expectURL string) {
			s.proxy.DeregisterReplicaIfTarget(slug, index, expectURL)
		}
	}
	if err := lifecycle.LoseWorkerReplicas(s.store, nodeID, deregister); err != nil {
		slog.Error("revoke worker: evict replicas", "node", nodeID, "err", err)
	}
	s.store.LogAuditEvent(db.AuditEventParams{
		UserID:       callerID(r),
		Action:       "revoke_worker",
		ResourceType: "worker",
		ResourceID:   nodeID,
		IPAddress:    s.ClientIP(r),
	})
	w.WriteHeader(http.StatusNoContent)
}
