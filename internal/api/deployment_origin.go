package api

import (
	"net/http"
	"strings"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
)

const (
	deploymentChannelHeader  = "X-Shinyhub-Deploy-Channel"
	developmentSessionHeader = "X-Shinyhub-Development-Session"
	developmentTargetHeader  = "X-Shinyhub-Development-Target"
	deploymentChannelWatch   = "watch"
)

// deploymentOriginForRequest converts request context into durable deployment
// attribution. A linked fleet run is authoritative; otherwise clients may
// identify their channel from a small allow-list. Unknown/direct API clients
// deliberately fall back to "api" rather than being mislabeled as dashboard.
func deploymentOriginForRequest(r *http.Request, runID string, rollback bool) db.DeploymentOrigin {
	origin := db.DeploymentOrigin{Kind: db.DeploymentOriginDirect, Channel: db.DeploymentChannelAPI}
	if rollback {
		origin.Kind = db.DeploymentOriginRollback
	}
	if runID != "" {
		origin.Kind = db.DeploymentOriginFleet
		origin.Channel = db.DeploymentChannelFleet
	}
	if origin.Kind != db.DeploymentOriginFleet {
		switch strings.ToLower(strings.TrimSpace(r.Header.Get(deploymentChannelHeader))) {
		case db.DeploymentChannelDashboard:
			origin.Channel = db.DeploymentChannelDashboard
		case db.DeploymentChannelCLI:
			origin.Channel = db.DeploymentChannelCLI
		case deploymentChannelWatch:
			// Persist watch attempts as direct CLI deployments for compatibility
			// with databases created before remote-development sessions existed.
			// The session ID is the durable, unambiguous watch marker.
			origin.Channel = db.DeploymentChannelCLI
		case db.DeploymentChannelAPI:
			origin.Channel = db.DeploymentChannelAPI
		}
	}
	if u := auth.UserFromContext(r.Context()); u != nil {
		id := u.ID
		origin.UserID = &id
		origin.Actor = u.Username
	}
	if credential := auth.CredentialInfoFromContext(r.Context()); credential != nil {
		origin.CredentialType = credential.Type
		origin.CredentialName = credential.Name
		if credential.ID != 0 {
			id := credential.ID
			origin.CredentialID = &id
		}
	}
	return origin
}
