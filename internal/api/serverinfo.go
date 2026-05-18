package api

import "net/http"

// serverInfoResponse is the JSON shape returned by GET /api/server-info.
type serverInfoResponse struct {
	Capabilities serverCapabilities `json:"capabilities"`
}

// serverCapabilities enumerates the optional protocol features this server
// supports. A fleet-aware CLI inspects these flags to decide whether it can
// rely on precondition headers and content-digest tracking, or must degrade
// gracefully against an older server.
type serverCapabilities struct {
	FleetPreconditions bool `json:"fleet_preconditions"`
	ContentDigest      bool `json:"content_digest"`
}

// handleServerInfo advertises server capability flags so a fleet-aware CLI
// can detect precondition + digest support and degrade gracefully against
// older servers. Unauthenticated and side-effect free.
func (s *Server) handleServerInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, serverInfoResponse{
		Capabilities: serverCapabilities{
			FleetPreconditions: true,
			ContentDigest:      true,
		},
	})
}
