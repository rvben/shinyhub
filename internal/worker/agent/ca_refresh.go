// internal/worker/agent/ca_refresh.go
package agent

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
)

// applyCABundle swaps a CA bundle the control plane returned on heartbeat into
// the holder so live listeners and clients verify against it on their next
// handshake, and persists it for restart re-adoption. Applying the bundle the
// worker already trusts is a no-op, so the common heartbeat path neither rebuilds
// the pool nor rewrites the file.
func (a *Agent) applyCABundle(caBundle string) error {
	changed, err := a.cacerts.Set([]byte(caBundle))
	if err != nil {
		return fmt.Errorf("apply ca bundle: %w", err)
	}
	if !changed {
		return nil
	}
	path := filepath.Join(a.cfg.DataDir, "agent", "ca-bundle.pem")
	if err := os.WriteFile(path, []byte(caBundle), 0o600); err != nil {
		return fmt.Errorf("persist ca bundle: %w", err)
	}
	slog.Info("worker applied rotated CA bundle", "node_id", a.nodeID)
	return nil
}
