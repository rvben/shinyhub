package lifecycle

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
)

// ReplicaRouteValidator protects adoption of persisted routes, including a
// handoff before ownership recovery runs. Database status alone is not proof
// that a process survived. Local native processes must still belong to their
// recorded bundle; every newly adopted endpoint must satisfy HTTP readiness.
func ReplicaRouteValidator(store *db.Store, mgr *process.Manager) func(context.Context, db.RoutableReplica, http.RoundTripper) error {
	return func(ctx context.Context, rr db.RoutableReplica, transport http.RoundTripper) error {
		r := rr.Replica
		bundleDir, err := replicaBundleDir(store, r)
		if err != nil {
			return err
		}
		if _, native := mgr.RuntimeForTier(r.Tier).(*process.NativeRuntime); native && r.WorkerID == "" {
			if r.PID == nil || bundleDir == "" {
				return fmt.Errorf("missing native process identity")
			}
			if err := validateNativeProcessIdentity(*r.PID, bundleDir); err != nil {
				return err
			}
		}
		if isolated, ok := mgr.RuntimeForTier(r.Tier).(*process.SystemdRuntime); ok {
			if r.WorkerID == "" {
				return fmt.Errorf("missing isolated native unit identity")
			}
			pid, err := isolated.InspectPID(r.WorkerID)
			if err != nil || pid <= 0 || (r.PID != nil && *r.PID != pid) {
				return fmt.Errorf("isolated native worker identity is not live")
			}
		}
		probeCtx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		return deploy.ProbeReadiness(probeCtx, r.EndpointURL, bundleDir, transport)
	}
}

func replicaBundleDir(store *db.Store, r *db.Replica) (string, error) {
	if r.DeploymentID == nil {
		return activeBundleDir(store, r.AppID), nil
	}
	d, err := store.GetDeploymentByID(*r.DeploymentID)
	if err != nil {
		return "", err
	}
	if d.AppID != r.AppID {
		return "", fmt.Errorf("replica deployment belongs to another app")
	}
	return d.BundleDir, nil
}

// Discard only the endpoint rejected by recovery, preserving a concurrent replacement.
func discardRecoveredRoute(prx *proxy.Proxy, app *db.App, r *db.Replica) {
	if prx == nil {
		return
	}
	target := r.EndpointURL
	if target == "" && r.Port != nil {
		target = fmt.Sprintf("http://127.0.0.1:%d", *r.Port)
	}
	if target != "" {
		prx.DeregisterReplicaIfTarget(app.Slug, r.Index, target)
	}
}

// A recovered runtime is owned immediately to avoid duplicate launches, but
// its user traffic waits for the same HTTP readiness contract as fresh boots.
func recoveredRouteTransport(prx *proxy.Proxy, store *db.Store, r *db.Replica, base http.RoundTripper, endpointURL string) http.RoundTripper {
	bundleDir, bundleErr := replicaBundleDir(store, r)
	return prx.NewReadinessTransport(base, func(ctx context.Context) error {
		if bundleErr != nil {
			return bundleErr
		}
		probeCtx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		return deploy.ProbeReadiness(probeCtx, endpointURL, bundleDir, base)
	})
}
