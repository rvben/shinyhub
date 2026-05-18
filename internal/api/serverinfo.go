package api

import "net/http"

// handleServerInfo advertises server capability flags so a fleet-aware CLI
// can detect precondition + digest support and degrade gracefully against
// older servers. Unauthenticated and side-effect free.
func (s *Server) handleServerInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"capabilities": map[string]bool{
			"fleet_preconditions": true,
			"content_digest":      true,
		},
	})
}
