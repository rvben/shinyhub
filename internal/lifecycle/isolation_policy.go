package lifecycle

import (
	"fmt"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
)

type coldIsolationStore interface {
	GetServingDeployment(int64) (*db.Deployment, error)
	GetDeploymentWorkerIsolation(int64) (string, error)
	RecordDeploymentWorkerIsolation(int64, int64, string) error
	ReplaceStoppedDeploymentWorkerIsolation(int64, int64, string) error
	ListDeploymentReplicas(int64) ([]*db.DeploymentReplica, error)
}

// PrepareColdWorkerIsolation preserves a recorded policy while any generation
// remains, and adopts desired policy only after a verified cold stop. Callers
// hold the app operation fence through boot.
func PrepareColdWorkerIsolation(store coldIsolationStore, manager *process.Manager, prx *proxy.Proxy, app *db.App, defaultIsolation string) (string, error) {
	dep, err := store.GetServingDeployment(app.ID)
	if err != nil {
		return "", err
	}
	desired := deploy.ResolveWorkerIsolation(app.WorkerIsolation, defaultIsolation)
	recorded, err := store.GetDeploymentWorkerIsolation(dep.ID)
	if err != nil {
		return "", err
	}
	if recorded == desired {
		return recorded, nil
	}
	if manager != nil && manager.HoldsLiveProcess(app.Slug) {
		if recorded != "" {
			return recorded, nil
		}
		return "", fmt.Errorf("cannot change serving worker isolation while a generation holds a live process")
	}
	rows, err := store.ListDeploymentReplicas(app.ID)
	if err != nil {
		return "", err
	}
	if len(rows) != 0 {
		if recorded != "" {
			return recorded, nil
		}
		return "", fmt.Errorf("cannot change serving worker isolation while generation cleanup is pending")
	}
	if prx != nil && (prx.HasDrainingGeneration(app.Slug) || prx.HasCandidateGeneration(app.Slug) || prx.HasServingBackends(app.Slug)) {
		if recorded != "" {
			return recorded, nil
		}
		return "", fmt.Errorf("cannot change serving worker isolation while generation routes remain")
	}
	if err := store.ReplaceStoppedDeploymentWorkerIsolation(app.ID, dep.ID, desired); err != nil {
		return "", err
	}
	return desired, nil
}
