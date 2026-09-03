package db_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

// seedReplica writes a replica row in an explicit runtime state. Every field a
// park is supposed to clear is populated, so a test can tell "parked" from
// "left alone" on more than the status column.
func seedReplica(t *testing.T, s *db.Store, appID int64, idx int, status, desired string) {
	t.Helper()
	pid, port := 4242+idx, 21000+idx
	if err := s.UpsertReplica(db.UpsertReplicaParams{
		AppID: appID, Index: idx, PID: &pid, Port: &port,
		Status: status, DesiredState: desired,
		Provider: "native", Tier: "default", AppVersion: "v1",
		EndpointURL: "http://127.0.0.1:21000", WorkerID: "worker-a",
	}); err != nil {
		t.Fatalf("UpsertReplica(%s/%s): %v", status, desired, err)
	}
}

func replicaAt(t *testing.T, s *db.Store, appID int64, idx int) *db.Replica {
	t.Helper()
	reps, err := s.ListReplicas(appID)
	if err != nil {
		t.Fatalf("ListReplicas: %v", err)
	}
	for _, r := range reps {
		if r.Index == idx {
			return r
		}
	}
	t.Fatalf("replica %d not found in %d rows", idx, len(reps))
	return nil
}

func assertParked(t *testing.T, r *db.Replica, what string) {
	t.Helper()
	if r.Status != "stopped" || r.DesiredState != "stopped" {
		t.Errorf("%s: status/desired = %q/%q, want stopped/stopped", what, r.Status, r.DesiredState)
	}
	if r.PID != nil || r.Port != nil || r.EndpointURL != "" || r.WorkerID != "" {
		t.Errorf("%s: routable identity survived: pid=%v port=%v endpoint=%q worker=%q",
			what, r.PID, r.Port, r.EndpointURL, r.WorkerID)
	}
	if r.Provider != "native" || r.Tier != "default" || r.AppVersion != "v1" {
		t.Errorf("%s: durable placement provenance lost: provider=%q tier=%q version=%q",
			what, r.Provider, r.Tier, r.AppVersion)
	}
}

func assertUntouched(t *testing.T, r *db.Replica, wantStatus, wantDesired, what string) {
	t.Helper()
	if r.Status != wantStatus || r.DesiredState != wantDesired {
		t.Errorf("%s: status/desired = %q/%q, want %q/%q (row must not be repaired)",
			what, r.Status, r.DesiredState, wantStatus, wantDesired)
	}
	if r.PID == nil || r.WorkerID == "" {
		t.Errorf("%s: runtime identity was cleared on a row that must be left alone: pid=%v worker=%q",
			what, r.PID, r.WorkerID)
	}
}

// A pre-0.15.3 recovery pass parked the app row but left its replica row
// crashed. Re-running the park must repair that row rather than abort: the
// guard exists to protect intentional states, not to reject work already half
// done.
func TestMarkRecoveryHibernated_RepairsAlreadyParkedApp(t *testing.T) {
	s := dbtest.New(t)
	owner := mustCreateUser(t, s, "park-owner", "developer")
	app := mustCreateApp(t, s, "park-inherited", owner.ID)

	seedReplica(t, s, app.ID, 0, "crashed", "running")
	if err := s.UpdateAppStatus(db.UpdateAppStatusParams{Slug: app.Slug, Status: "hibernated"}); err != nil {
		t.Fatal(err)
	}

	if err := s.MarkRecoveryHibernated(app.Slug); err != nil {
		t.Fatalf("MarkRecoveryHibernated on an already-parked app: %v", err)
	}
	assertParked(t, replicaAt(t, s, app.ID, 0), "inherited replica")

	got, err := s.GetAppBySlug(app.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "hibernated" {
		t.Errorf("app status = %q, want hibernated", got.Status)
	}
}

// The park must never overrule an intentional or terminal app state: a stopped
// app stays stopped, and a genuinely broken app stays crashed so an operator
// still sees the failure.
func TestMarkRecoveryHibernated_LeavesDecidedStatesAlone(t *testing.T) {
	for _, status := range []string{"stopped", "crashed"} {
		t.Run(status, func(t *testing.T) {
			s := dbtest.New(t)
			owner := mustCreateUser(t, s, "decided-owner", "developer")
			app := mustCreateApp(t, s, "decided-"+status, owner.ID)

			seedReplica(t, s, app.ID, 0, "crashed", "running")
			if err := s.UpdateAppStatus(db.UpdateAppStatusParams{Slug: app.Slug, Status: status}); err != nil {
				t.Fatal(err)
			}

			if err := s.MarkRecoveryHibernated(app.Slug); !errors.Is(err, db.ErrNotFound) {
				t.Fatalf("MarkRecoveryHibernated on a %s app = %v, want ErrNotFound", status, err)
			}
			got, err := s.GetAppBySlug(app.Slug)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != status {
				t.Errorf("app status = %q, want %q", got.Status, status)
			}
		})
	}
}

// parkedSlugs flattens a repair report to the app slugs it names, so a test can
// assert the repaired set independently of which slot each repair landed on.
func parkedSlugs(repaired []db.ParkedReplica) []string {
	out := make([]string, 0, len(repaired))
	for _, r := range repaired {
		out = append(out, r.Slug)
	}
	return out
}

// The sweep repairs replica rows that contradict a non-serving app, and only
// those: a crash under a serving app is live information the watchdog acts on,
// a warm row is an intentional park, and a lost row is indeterminate (its
// process may still be alive, so its identity must survive).
//
// All three non-serving statuses are exercised. They are separate values in the
// predicate, so a regression dropping one would otherwise leave every app in
// that state stranded with nothing to catch it.
func TestParkStrandedReplicas(t *testing.T) {
	s := dbtest.New(t)
	owner := mustCreateUser(t, s, "sweep-owner", "developer")

	var stranded []*db.App
	for _, status := range []string{"hibernated", "suspended", "stopped"} {
		app := mustCreateApp(t, s, "sweep-"+status, owner.ID)
		seedReplica(t, s, app.ID, 0, "crashed", "running")
		if err := s.UpdateAppStatus(db.UpdateAppStatusParams{Slug: app.Slug, Status: status}); err != nil {
			t.Fatal(err)
		}
		stranded = append(stranded, app)
	}

	serving := mustCreateApp(t, s, "sweep-serving", owner.ID)
	seedReplica(t, s, serving.ID, 0, "crashed", "running")
	if err := s.UpdateAppStatus(db.UpdateAppStatusParams{Slug: serving.Slug, Status: "running"}); err != nil {
		t.Fatal(err)
	}

	warm := mustCreateApp(t, s, "sweep-warm", owner.ID)
	seedReplica(t, s, warm.ID, 0, "suspended", "warm")
	if err := s.UpdateAppStatus(db.UpdateAppStatusParams{Slug: warm.Slug, Status: "hibernated"}); err != nil {
		t.Fatal(err)
	}

	lost := mustCreateApp(t, s, "sweep-lost", owner.ID)
	seedReplica(t, s, lost.ID, 0, "lost", "running")
	if err := s.UpdateAppStatus(db.UpdateAppStatusParams{Slug: lost.Slug, Status: "hibernated"}); err != nil {
		t.Fatal(err)
	}

	repaired, err := s.ParkStrandedReplicas()
	if err != nil {
		t.Fatalf("ParkStrandedReplicas: %v", err)
	}
	want := []string{"sweep-hibernated", "sweep-stopped", "sweep-suspended"}
	if got := parkedSlugs(repaired); !slices.Equal(got, want) {
		t.Fatalf("repaired = %v, want exactly %v", got, want)
	}
	for _, r := range repaired {
		if r.Index != 0 {
			t.Errorf("repaired %s: idx = %d, want the slot that was actually parked (0)", r.Slug, r.Index)
		}
	}

	for _, app := range stranded {
		assertParked(t, replicaAt(t, s, app.ID, 0), "stranded replica under a "+app.Slug+" app")
	}
	assertUntouched(t, replicaAt(t, s, serving.ID, 0), "crashed", "running", "crash under a serving app")
	assertUntouched(t, replicaAt(t, s, warm.ID, 0), "suspended", "warm", "warm-parked replica")
	assertUntouched(t, replicaAt(t, s, lost.ID, 0), "lost", "running", "lost replica")

	// Idempotent: a second sweep finds nothing left to repair.
	again, err := s.ParkStrandedReplicas()
	if err != nil {
		t.Fatalf("second ParkStrandedReplicas: %v", err)
	}
	if len(again) != 0 {
		t.Errorf("second sweep repaired %v, want none", again)
	}
}

// The exit diagnostics are the operator's only record of why the process
// disappeared, so repairing the row must not erase them.
func TestParkStrandedReplicas_PreservesExitDiagnostics(t *testing.T) {
	s := dbtest.New(t)
	owner := mustCreateUser(t, s, "diag-owner", "developer")
	app := mustCreateApp(t, s, "diag-app", owner.ID)

	seedReplica(t, s, app.ID, 0, "running", "running")
	if err := s.RecordReplicaCrash(db.UpsertReplicaParams{
		AppID: app.ID, Index: 0, Status: "crashed", Reason: "process not alive",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateAppStatus(db.UpdateAppStatusParams{Slug: app.Slug, Status: "hibernated"}); err != nil {
		t.Fatal(err)
	}

	if _, err := s.ParkStrandedReplicas(); err != nil {
		t.Fatalf("ParkStrandedReplicas: %v", err)
	}

	r := replicaAt(t, s, app.ID, 0)
	assertParked(t, r, "stranded replica")
	if r.LastExit == nil {
		t.Fatal("last_exit was erased by the repair")
	}
	if r.LastExit.Reason != "process not alive" || r.LastExit.CrashCount != 1 {
		t.Errorf("last_exit = %+v, want reason=%q crash_count=1", r.LastExit, "process not alive")
	}
}

// A replica a live schedule activation owns belongs to that activation's
// rollout, which repairs its own slots. Parking it underneath would strand the
// roll.
func TestParkStrandedReplicas_SkipsLiveActivationOwnedRows(t *testing.T) {
	s := dbtest.New(t)
	owner := mustCreateUser(t, s, "act-owner", "developer")
	app := mustCreateApp(t, s, "act-app", owner.ID)

	seedReplica(t, s, app.ID, 0, "crashed", "running")
	if err := s.UpdateAppStatus(db.UpdateAppStatusParams{Slug: app.Slug, Status: "hibernated"}); err != nil {
		t.Fatal(err)
	}

	// RETURNING, not LastInsertId: the pgx driver does not support LastInsertId,
	// so the id must come back from the statement for this test to run under
	// SHINYHUB_TEST_POSTGRES_DSN - which is the only way the carve-out is
	// verified on the dialect the suite does not exercise by default.
	var activationID int64
	if err := s.DB().QueryRow(`INSERT INTO schedule_activations
		(app_id, app_slug, schedule_name, action, target_generation, status, due_at)
		VALUES (?, ?, 'refresh', 'roll', 1, 'running', CURRENT_TIMESTAMP)
		RETURNING id`, app.ID, app.Slug).Scan(&activationID); err != nil {
		t.Fatalf("insert activation: %v", err)
	}
	if _, err := s.DB().Exec(`UPDATE replicas SET activation_id = ? WHERE app_id = ? AND idx = 0`,
		activationID, app.ID); err != nil {
		t.Fatalf("link activation: %v", err)
	}

	repaired, err := s.ParkStrandedReplicas()
	if err != nil {
		t.Fatalf("ParkStrandedReplicas: %v", err)
	}
	if len(repaired) != 0 {
		t.Errorf("repaired = %v, want none while the activation is live", repaired)
	}
	assertUntouched(t, replicaAt(t, s, app.ID, 0), "crashed", "running", "activation-owned replica")

	// Once the activation reaches a terminal status it no longer owns the slot,
	// so the same row becomes repairable.
	if _, err := s.DB().Exec(`UPDATE schedule_activations SET status = 'failed' WHERE id = ?`,
		activationID); err != nil {
		t.Fatalf("finish activation: %v", err)
	}
	repaired, err = s.ParkStrandedReplicas()
	if err != nil {
		t.Fatalf("ParkStrandedReplicas after activation finished: %v", err)
	}
	if got := parkedSlugs(repaired); !slices.Equal(got, []string{app.Slug}) {
		t.Fatalf("repaired = %v, want [%s] once the activation is terminal", got, app.Slug)
	}
	assertParked(t, replicaAt(t, s, app.ID, 0), "released replica")
}
