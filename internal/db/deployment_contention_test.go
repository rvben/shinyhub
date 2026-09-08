package db_test

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

// Use a real WAL database and independent connections: the in-memory fixture's
// single connection cannot reproduce a deferred read-to-write upgrade failure.
func TestDeploymentPublicationWithConcurrentWriters(t *testing.T) {
	dbtest.SkipIfPostgres(t)
	path := filepath.Join(t.TempDir(), "deployments.db")
	store, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	writer, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	owner := mustCreateUser(t, store, "contention-owner", "developer")
	type scenario struct {
		appID, previousID int64
		candidates        []*db.Deployment
	}
	cases := make([]scenario, 8)
	for i := range cases {
		app := mustCreateApp(t, store, fmt.Sprintf("contention-%d", i), owner.ID)
		previous, err := store.BeginDeployment(app.ID, "old", "/bundles/old")
		if err != nil {
			t.Fatal(err)
		}
		if err := store.PromoteDeployment(previous.ID); err != nil {
			t.Fatal(err)
		}
		cases[i] = scenario{appID: app.ID, previousID: previous.ID}
		for j := 0; j < 8; j++ {
			candidate, err := store.BeginDeployment(app.ID, fmt.Sprintf("new-%d", j), fmt.Sprintf("/bundles/new-%d", j))
			if err != nil {
				t.Fatal(err)
			}
			cases[i].candidates = append(cases[i].candidates, candidate)
		}
	}
	stop := make(chan struct{})
	writerDone := make(chan error, 1)
	go func() {
		for {
			select {
			case <-stop:
				writerDone <- nil
				return
			default:
			}
			if _, err := writer.DB().Exec(`UPDATE apps SET updated_at = CURRENT_TIMESTAMP WHERE id = ?`, cases[0].appID); err != nil {
				writerDone <- err
				return
			}
		}
	}()
	var wg sync.WaitGroup
	failures := make(chan error, len(cases))
	for _, c := range cases {
		wg.Go(func() {
			for i, candidate := range c.candidates {
				if err := store.PromoteDeployment(candidate.ID); err != nil {
					failures <- err
					return
				}
				// An ambiguous acknowledgement can be retried while other apps write.
				if err := store.PromoteDeployment(candidate.ID); err != nil {
					failures <- err
					return
				}
				if i != len(c.candidates)-1 {
					if err := store.RevertDeploymentActivation(candidate.ID, c.previousID, "publication failed"); err != nil {
						failures <- err
						return
					}
					active, err := store.GetActiveDeploymentGeneration(c.appID)
					if err != nil {
						failures <- err
						return
					}
					if active.DeploymentID != c.previousID {
						failures <- fmt.Errorf("compensation selected %d, want %d", active.DeploymentID, c.previousID)
						return
					}
				}
			}
		})
	}
	wg.Wait()
	close(stop)
	if err := <-writerDone; err != nil {
		t.Error(err)
	}
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	if t.Failed() {
		return
	}
	// A fresh store models the durable generation lookup used after restart or
	// wake; the successfully published version must survive connection teardown.
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	for _, c := range cases {
		want := c.candidates[len(c.candidates)-1]
		active, err := reopened.GetActiveDeploymentGeneration(c.appID)
		if err != nil {
			t.Fatal(err)
		}
		if active.DeploymentID != want.ID || active.ActivationToken != want.ActivationToken {
			t.Fatalf("reopened generation = %+v, want deployment %d", active, want.ID)
		}
		deployments, err := reopened.ListDeployments(c.appID)
		if err != nil {
			t.Fatal(err)
		}
		if len(deployments) == 0 || deployments[0].Version != want.Version || deployments[0].BundleDir != want.BundleDir {
			t.Fatalf("restart/wake bundle = %+v, want %s at %s", deployments, want.Version, want.BundleDir)
		}
	}
}
