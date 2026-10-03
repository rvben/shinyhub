// internal/worker/agent/ca_refresh.go
package agent

import (
	"fmt"
	"log/slog"
	"path/filepath"
)

// applyCABundle swaps a CA bundle the control plane returned on heartbeat into
// the holder so live listeners and clients verify against it on their next
// handshake, and persists it for restart re-adoption. Applying the bundle the
// worker already trusts is a no-op, so the common heartbeat path neither rebuilds
// the pool nor rewrites the file.
func (a *Agent) applyCABundle(caBundle string) error {
	path := filepath.Join(a.cfg.DataDir, "agent", "ca-bundle.pem")
	var dirWarn error
	changed, err := a.cacerts.SetPersisted([]byte(caBundle), func(pem []byte) error {
		var perr error
		dirWarn, perr = persistAtomically(path, pem, 0o600)
		return perr
	})
	if err != nil {
		return fmt.Errorf("apply ca bundle: %w", err)
	}
	if !changed {
		return nil
	}
	if dirWarn != nil {
		slog.Warn("worker applied rotated CA bundle: parent directory fsync failed after rename", "node_id", a.nodeID, "err", dirWarn)
	}
	slog.Info("worker applied rotated CA bundle", "node_id", a.nodeID)
	return nil
}
