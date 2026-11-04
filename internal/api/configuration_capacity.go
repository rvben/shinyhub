package api

import (
	"strings"

	"github.com/rvben/shinyhub/internal/db"
)

// The control-plane CPU count is useful only when it is also the app host.
// Never present it as remote-worker or managed-runtime capacity.
func (s *Server) configurationUsesLocalHost(app *db.App) bool {
	if s.clustered || s.nodeForTier != nil {
		return false
	}
	for _, tier := range s.tiersForApp(app) {
		mode, known := s.cfg.Runtime.RuntimeForTier(tier)
		if !known {
			if len(s.cfg.Runtime.Tiers) > 0 {
				return false
			}
			mode = s.cfg.Runtime.Mode
		}
		if mode != "" && mode != "native" && mode != "docker" {
			return false
		}
		if mode == "docker" && s.cfg.Runtime.Docker.Socket != "" && !strings.HasPrefix(s.cfg.Runtime.Docker.Socket, "/") && !strings.HasPrefix(s.cfg.Runtime.Docker.Socket, "unix://") {
			return false
		}
	}
	return true
}

func (s *Server) configurationHostCPUCores(app *db.App) *float64 {
	if !s.configurationUsesLocalHost(app) || s.hostCapacity == nil || s.hostCapacity.Cores <= 0 {
		return nil
	}
	cores := s.hostCapacity.Cores
	return &cores
}
