package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/fleet"
)

type fleetRecoverySnapshot struct {
	App                      *db.App `json:"app"`
	Revision                 string  `json:"resource_revision"`
	CompatibilityQuarantined *bool   `json:"compatibility_quarantined"`
}

func readFleetRecoverySnapshot(cfg *cliConfig, slug string, timeout time.Duration) (*fleetRecoverySnapshot, error) {
	ctx, cancel := context.WithTimeout(context.Background(), outcomeTimeout(timeout))
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.Host+"/api/apps/"+slug, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", authHeader(cfg.Token))
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("read deployment repair state: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, httpError(cfg.Token, "read deployment repair state", resp, body)
	}
	var state fleetRecoverySnapshot
	if err := json.NewDecoder(resp.Body).Decode(&state); err != nil {
		return nil, &protocolError{op: "decode deployment repair state", err: err}
	}
	if state.App == nil || state.App.DeploymentRepairRequired == nil || state.Revision == "" || state.CompatibilityQuarantined == nil {
		return nil, &protocolError{op: "decode deployment repair state", err: fmt.Errorf("server omitted authoritative repair state or resource revision")}
	}
	return &state, nil
}

func recoveryOwnerMatches(state *fleetRecoverySnapshot, marker string) bool {
	return state.App.ManagedBy != nil && *state.App.ManagedBy == marker
}

func recoveryConverged(state *fleetRecoverySnapshot, d fleet.AppDiff, entry fleet.AppEntry, marker string) bool {
	return !*state.App.DeploymentRepairRequired && !*state.CompatibilityQuarantined &&
		state.App.LastDeploymentStatus != "pending" && recoveryOwnerMatches(state, marker) &&
		state.App.ContentDigest == d.LocalDigest && persistedAppStatus(*state.App) != "failed" &&
		len(fleet.ConfigDrift(entry, observedFromApp(*state.App))) == 0
}

func recoveryStillNeeded(state *fleetRecoverySnapshot, d fleet.AppDiff, entry fleet.AppEntry, marker string) bool {
	return *state.App.DeploymentRepairRequired && state.App.LastDeploymentStatus != "pending" &&
		persistedAppStatus(*state.App) != "stopped" && recoveryOwnerMatches(state, marker) &&
		state.App.ContentDigest == d.LocalDigest &&
		slices.Equal(d.ConfigDrift, fleet.ConfigDrift(entry, observedFromApp(*state.App)))
}

func recoveryStateConflict(slug string) error {
	return &conflictError{slug: slug, msg: "deployment repair state, source, owner or settings changed; re-run plan before applying"}
}
