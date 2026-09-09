package api

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/rvben/shinyhub/internal/db"
	workerapi "github.com/rvben/shinyhub/internal/worker/api"
)

// HandleElasticAuthorize checks each worker command against the shared live
// controller lease and reservation binding. The worker identity comes from the
// verified mTLS peer, never from the command's claimed node identifier.
func (a *WorkerAPI) HandleElasticAuthorize(w http.ResponseWriter, r *http.Request) {
	nodeID, ok := a.authenticatedNodeID(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "worker authentication required")
		return
	}
	var command workerapi.ElasticAuthority
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	if err := decoder.Decode(&command); err != nil {
		writeError(w, http.StatusBadRequest, "invalid authorization request")
		return
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid authorization request")
		return
	}
	if command.NodeID != nodeID {
		writeError(w, http.StatusForbidden, "worker identity mismatch")
		return
	}
	owner := db.ElasticOwner{Instance: command.Instance, Epoch: command.Epoch}
	if err := a.store.AuthorizeElasticCommand(r.Context(), owner, command.ReservationID, nodeID, command.RequestDigest, command.Action); err != nil {
		writeError(w, http.StatusConflict, "elastic authorization refused")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
