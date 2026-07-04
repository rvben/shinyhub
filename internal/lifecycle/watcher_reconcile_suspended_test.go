package lifecycle

import (
	"testing"

	"github.com/rvben/shinyhub/internal/db"
)

// A suspended replica that is a warm-floor victim (DesiredState=warm) is
// intentional capacity, so the app stays running. This pins the existing
// reconcileAppStatus tally against regression.
func TestReconcile_SuspendedWarmVictim_StaysRunning(t *testing.T) {
	st := newFakeStore(map[string]*db.App{"app": {ID: 1, Slug: "app", Status: "running", Replicas: 2}}, nil)
	st.replicas = map[int64][]*db.Replica{1: {
		{Index: 0, Status: db.ReplicaStatusRunning, DesiredState: "running"},
		{Index: 1, Status: db.ReplicaStatusSuspended, DesiredState: db.ReplicaDesiredWarm},
	}}
	w := newTestWatcher(Config{}, &fakeManager{}, newFakeProxy(), st, nil)
	app, _ := st.GetAppBySlug("app")

	w.reconcileAppStatus(app, st.replicas[1])

	if got := st.appStatus["app"]; got != "running" {
		t.Fatalf("app status = %q, want running (warm victim is intentional)", got)
	}
}

// A suspended replica that is NOT a warm victim under a running app is a hidden
// outage and must surface as degraded.
func TestReconcile_SuspendedNonWarm_MarksDegraded(t *testing.T) {
	st := newFakeStore(map[string]*db.App{"app": {ID: 1, Slug: "app", Status: "running", Replicas: 2}}, nil)
	st.replicas = map[int64][]*db.Replica{1: {
		{Index: 0, Status: db.ReplicaStatusRunning, DesiredState: "running"},
		{Index: 1, Status: db.ReplicaStatusSuspended, DesiredState: "running"},
	}}
	w := newTestWatcher(Config{}, &fakeManager{}, newFakeProxy(), st, nil)
	app, _ := st.GetAppBySlug("app")

	w.reconcileAppStatus(app, st.replicas[1])

	if got := st.appStatus["app"]; got != "degraded" {
		t.Fatalf("app status = %q, want degraded (suspended non-warm must be visible)", got)
	}
}
