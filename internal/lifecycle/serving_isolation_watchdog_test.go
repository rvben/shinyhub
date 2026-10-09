package lifecycle

import (
	"context"
	"errors"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"testing"
	"time"
)

type generationPolicyTestStore struct {
	*fakeStore
	mode      string
	policyErr error
}

func (s *generationPolicyTestStore) GetDeploymentWorkerIsolation(int64) (string, error) {
	return s.mode, s.policyErr
}

func TestWatchdogIgnoresOutgoingGenerationCrash(t *testing.T) {
	for _, mode := range []string{"grouped", "multiplex"} {
		t.Run(mode, func(t *testing.T) {
			app := &db.App{ID: 1, Slug: "demo", Status: "running", Replicas: 1, WorkerIsolation: "multiplex"}
			st := &generationPolicyTestStore{fakeStore: newFakeStore(map[string]*db.App{"demo": app}, []*db.Deployment{{ID: 202, AppID: 1, Status: db.DeploymentSucceeded, BundleDir: "/tmp/demo"}}), mode: mode}
			boots := 0
			w := newTestWatcher(Config{RestartMaxAttempts: 1}, &fakeManager{}, newFakeProxy(), st.fakeStore, func(context.Context, string, string, int) (*deploy.Result, error) {
				boots++
				return &deploy.Result{}, nil
			})
			w.store = st
			for i := 0; i < 4; i++ {
				w.handleCrashedGeneration("demo", 0, 101)
			}
			if boots != 0 || appStatusOf(st.fakeStore, "demo") != "running" {
				t.Fatalf("old crash changed target: boots=%d status=%s", boots, appStatusOf(st.fakeStore, "demo"))
			}
			if len(w.crashCount) != 0 {
				t.Fatal("outgoing crash spent serving budget")
			}
		})
	}
}

func TestWatchdogUsesRecordedIsolationAcrossDesiredDefaultDrift(t *testing.T) {
	app := &db.App{ID: 1, Slug: "demo", Status: "running", Replicas: 1}
	st := &generationPolicyTestStore{fakeStore: newFakeStore(map[string]*db.App{"demo": app}, []*db.Deployment{{ID: 202, AppID: 1, Status: db.DeploymentSucceeded}}), mode: "multiplex"}
	w := newTestWatcher(Config{DefaultWorkerIsolation: "grouped"}, &fakeManager{}, newFakeProxy(), st.fakeStore, nil)
	w.store = st
	if got := w.servingIsolation(app); got != "multiplex" {
		t.Fatalf("serving mode=%s", got)
	}
	if !w.servingFixedReplica(app, 202) {
		t.Fatal("recorded fixed generation rejected")
	}
}

func TestWatchdogPolicyReadFailurePreservesHibernateRoutes(t *testing.T) {
	app := &db.App{ID: 1, Slug: "demo", Status: "running", Replicas: 1, UpdatedAt: time.Now().Add(-time.Hour)}
	st := &generationPolicyTestStore{fakeStore: newFakeStore(map[string]*db.App{"demo": app}, []*db.Deployment{{ID: 202, AppID: 1, Status: db.DeploymentSucceeded}}), policyErr: errors.New("policy unavailable")}
	prx := newFakeProxy()
	w := newTestWatcher(Config{HibernateTimeout: time.Minute}, &fakeManager{}, prx, st.fakeStore, nil)
	w.store = st
	w.handleIdle(app.Slug, 1)
	if len(prx.hibernated) != 0 || len(prx.deregistered) != 0 {
		t.Fatal("failed policy read removed routes")
	}
	if appStatusOf(st.fakeStore, app.Slug) != "running" {
		t.Fatal("failed policy read changed serving status")
	}
}
