// Package autoscalespec holds the pure, I/O-free validation for a declared
// per-app autoscale policy. It is the single source of truth shared by the
// bundle manifest ([app] autoscale in internal/deploy) and the fleet manifest
// ([[app]].config.autoscale in internal/fleet), so the two cannot drift. It
// mirrors the internal/schedulespec pattern.
package autoscalespec

import (
	"errors"
	"fmt"
	"math"
)

// MaxReplicasCeiling is the stored-column bound for the autoscale replica
// counts. It matches the apps table CHECK and the PATCH /api/apps handler; the
// runtime autoscale.max_replicas ceiling is a separate, config-dependent check.
const MaxReplicasCeiling = 1000

// Params is a declared autoscale policy. Enabled is a pointer so a declared
// block must state it explicitly - this rejects an incomplete block (e.g. one
// that sets only target) that would otherwise persist an all-zero policy.
type Params struct {
	Enabled     *bool
	MinReplicas int
	MaxReplicas int
	Target      float64
}

// Validate enforces the field bounds of a declared autoscale block. The runtime
// MaxReplicas ceiling needs server config and is checked by the caller. Bounds
// are range-checked even when disabled so a stored value is never out of the
// column range on a later re-enable.
func Validate(p Params) error {
	if p.Enabled == nil {
		return errors.New("autoscale.enabled is required when the autoscale block is present (true or false)")
	}
	// TOML permits the special floats nan/inf. NaN compares false to every
	// bound, so reject non-finite targets explicitly before the range check.
	if math.IsNaN(p.Target) || math.IsInf(p.Target, 0) {
		return fmt.Errorf("autoscale.target must be a finite number in [0,1], got %g", p.Target)
	}
	if p.Target < 0 || p.Target > 1 {
		return fmt.Errorf("autoscale.target must be in [0,1] (0 inherits the runtime default), got %g", p.Target)
	}
	if p.MinReplicas < 0 || p.MinReplicas > MaxReplicasCeiling {
		return fmt.Errorf("autoscale.min_replicas must be between 0 and %d, got %d", MaxReplicasCeiling, p.MinReplicas)
	}
	if p.MaxReplicas < 0 || p.MaxReplicas > MaxReplicasCeiling {
		return fmt.Errorf("autoscale.max_replicas must be between 0 and %d, got %d", MaxReplicasCeiling, p.MaxReplicas)
	}
	if *p.Enabled {
		if p.MinReplicas < 1 {
			return fmt.Errorf("autoscale.min_replicas must be >= 1 when enabled, got %d", p.MinReplicas)
		}
		if p.MaxReplicas < p.MinReplicas {
			return fmt.Errorf("autoscale.max_replicas must be >= min_replicas, got max=%d min=%d", p.MaxReplicas, p.MinReplicas)
		}
	}
	return nil
}
