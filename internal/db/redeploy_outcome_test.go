package db_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

func intp(v int) *int { return &v }

// redeployApp creates a running app on a fresh store and returns it.
func newRedeployApp(t *testing.T, s *db.Store, slug string) *db.App {
	t.Helper()
	u := mustCreateUser(t, s, "owner-"+slug, "admin")
	app := mustCreateApp(t, s, slug, u.ID)
	if err := s.UpdateAppStatus(db.UpdateAppStatusParams{Slug: slug, Status: "running"}); err != nil {
		t.Fatal(err)
	}
	return app
}

func mustGetApp(t *testing.T, s *db.Store, slug string) *db.App {
	t.Helper()
	app, err := s.GetAppBySlug(slug)
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func TestPatchAppSettings_ArmsRedeployOnlyForPoolShapeChangeOnServingApp(t *testing.T) {
	cases := []struct {
		name   string
		status string
		params db.PatchAppSettingsParams
		armed  bool
		// full: the armed seq needs the whole pool cycled, not a resize.
		full bool
	}{
		{"replicas changed", "running", db.PatchAppSettingsParams{SetReplicas: true, Replicas: 2, ArmRedeploy: true}, true, false},
		{"replicas unchanged", "running", db.PatchAppSettingsParams{SetReplicas: true, Replicas: 1, ArmRedeploy: true}, false, false},
		{"memory limit set", "running", db.PatchAppSettingsParams{SetMemoryLimitMB: true, MemoryLimitMB: intp(256), ArmRedeploy: true}, true, true},
		{"memory limit already inherited", "running", db.PatchAppSettingsParams{SetMemoryLimitMB: true, ArmRedeploy: true}, false, false},
		{"cpu quota set", "running", db.PatchAppSettingsParams{SetCPUQuotaPercent: true, CPUQuotaPercent: intp(50), ArmRedeploy: true}, true, true},
		{"worker isolation changed", "running", db.PatchAppSettingsParams{SetWorkerIsolation: true, WorkerIsolation: "per_session", ArmRedeploy: true}, true, true},
		{"placement set", "running", db.PatchAppSettingsParams{SetPlacement: true, Placement: `{"gpu":2}`, PlacementTotal: 2, ArmRedeploy: true}, true, true},
		{"replicas and memory changed", "running", db.PatchAppSettingsParams{SetReplicas: true, Replicas: 2, SetMemoryLimitMB: true, MemoryLimitMB: intp(256), ArmRedeploy: true}, true, true},
		{"warm spares only", "running", db.PatchAppSettingsParams{SetWorkerWarmSpares: true, WorkerWarmSpares: 2, ArmRedeploy: true}, false, false},
		{"hibernate only", "running", db.PatchAppSettingsParams{SetHibernate: true, HibernateMinutes: intp(5), ArmRedeploy: true}, false, false},
		{"not asked to arm", "running", db.PatchAppSettingsParams{SetReplicas: true, Replicas: 2}, false, false},
		{"stopped app", "stopped", db.PatchAppSettingsParams{SetReplicas: true, Replicas: 2, ArmRedeploy: true}, false, false},
		{"hibernated app", "hibernated", db.PatchAppSettingsParams{SetReplicas: true, Replicas: 2, ArmRedeploy: true}, false, false},
		{"degraded app", "degraded", db.PatchAppSettingsParams{SetReplicas: true, Replicas: 2, ArmRedeploy: true}, true, false},
		{"degraded app memory set", "degraded", db.PatchAppSettingsParams{SetMemoryLimitMB: true, MemoryLimitMB: intp(256), ArmRedeploy: true}, true, true},
		{"degraded app replicas repaired", "degraded", db.PatchAppSettingsParams{SetReplicas: true, Replicas: 1, ArmRedeploy: true}, true, false},
		{"crashed app", "crashed", db.PatchAppSettingsParams{SetReplicas: true, Replicas: 2, ArmRedeploy: true}, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := dbtest.New(t)
			newRedeployApp(t, s, "arm")
			if err := s.UpdateAppStatus(db.UpdateAppStatusParams{Slug: "arm", Status: tc.status}); err != nil {
				t.Fatal(err)
			}
			p := tc.params
			p.Slug = "arm"
			res, err := s.PatchAppSettings(p)
			if err != nil {
				t.Fatal(err)
			}
			got := mustGetApp(t, s, "arm").RedeploySeqLaunched
			if tc.armed {
				if res.RedeploySeq != 1 || got != 1 {
					t.Fatalf("armed: result seq %d, stored seq %d, want 1 and 1", res.RedeploySeq, got)
				}
			} else if res.RedeploySeq != 0 || got != 0 {
				t.Fatalf("not armed: result seq %d, stored seq %d, want 0 and 0", res.RedeploySeq, got)
			}
			full, err := s.RedeployNeedsFullCycle(context.Background(), "arm")
			if err != nil {
				t.Fatal(err)
			}
			if full != tc.full {
				t.Fatalf("needs full cycle = %v, want %v", full, tc.full)
			}
		})
	}
}

func TestPatchAppSettings_RedeploySeqIsMonotonic(t *testing.T) {
	s := dbtest.New(t)
	newRedeployApp(t, s, "mono")
	for i, replicas := range []int{2, 3, 1} {
		res, err := s.PatchAppSettings(db.PatchAppSettingsParams{Slug: "mono", SetReplicas: true, Replicas: replicas, ArmRedeploy: true})
		if err != nil {
			t.Fatal(err)
		}
		if want := int64(i + 1); res.RedeploySeq != want {
			t.Fatalf("patch %d armed seq %d, want %d", i, res.RedeploySeq, want)
		}
	}
	if app := mustGetApp(t, s, "mono"); app.RedeploySeqLaunched != 3 || app.LastRedeploy != nil {
		t.Fatalf("launched %d last %+v, want 3 and no outcome yet", app.RedeploySeqLaunched, app.LastRedeploy)
	}
}

// failOn installs a trigger that aborts the statement it names, standing in
// for a database failure part-way through a transaction.
func failOn(t *testing.T, s *db.Store, sqliteEvent, table string) {
	t.Helper()
	if s.IsPostgres() {
		stmts := []string{
			`CREATE FUNCTION shinyhub_test_boom() RETURNS trigger AS $$ BEGIN RAISE EXCEPTION 'boom'; END $$ LANGUAGE plpgsql`,
			`CREATE TRIGGER shinyhub_test_boom BEFORE ` + sqliteEvent + ` ON ` + table + ` FOR EACH ROW EXECUTE FUNCTION shinyhub_test_boom()`,
		}
		for _, q := range stmts {
			if _, err := s.DB().Exec(q); err != nil {
				t.Fatal(err)
			}
		}
		return
	}
	if _, err := s.DB().Exec(`CREATE TRIGGER shinyhub_test_boom BEFORE ` + sqliteEvent + ` ON ` + table + ` BEGIN SELECT RAISE(ABORT, 'boom'); END`); err != nil {
		t.Fatal(err)
	}
}

// A settings write that fails must leave nothing committed, so an error from
// PatchAppSettings always means no settings changed and no redeploy is owed.
func TestPatchAppSettings_ProjectUpsertFailureRollsBackSettingsAndSeq(t *testing.T) {
	s := dbtest.New(t)
	newRedeployApp(t, s, "proj")
	failOn(t, s, "INSERT", "projects")
	_, err := s.PatchAppSettings(db.PatchAppSettingsParams{
		Slug: "proj", SetReplicas: true, Replicas: 3,
		SetProjectSlug: true, ProjectSlug: "analytics", ArmRedeploy: true,
	})
	if err == nil {
		t.Fatal("PatchAppSettings succeeded although the project upsert failed")
	}
	app := mustGetApp(t, s, "proj")
	if app.Replicas != 1 || app.RedeploySeqLaunched != 0 || app.ProjectSlug != "" {
		t.Fatalf("after failed write: replicas %d seq %d project %q, want 1, 0, \"\"", app.Replicas, app.RedeploySeqLaunched, app.ProjectSlug)
	}
}

func TestPatchAppSettings_PlacementFailureRollsBackSettingsAndSeq(t *testing.T) {
	s := dbtest.New(t)
	newRedeployApp(t, s, "place")
	failOn(t, s, "UPDATE OF replica_placement", "apps")
	_, err := s.PatchAppSettings(db.PatchAppSettingsParams{
		Slug: "place", SetMemoryLimitMB: true, MemoryLimitMB: intp(512),
		SetPlacement: true, Placement: `{"gpu":2}`, PlacementTotal: 2, ArmRedeploy: true,
	})
	if err == nil {
		t.Fatal("PatchAppSettings succeeded although the placement write failed")
	}
	app := mustGetApp(t, s, "place")
	if app.MemoryLimitMB != nil || app.ReplicaPlacement != "" || app.Replicas != 1 || app.RedeploySeqLaunched != 0 {
		t.Fatalf("after failed write: mem %v placement %q replicas %d seq %d, want all unchanged",
			app.MemoryLimitMB, app.ReplicaPlacement, app.Replicas, app.RedeploySeqLaunched)
	}
}

func TestPatchAppSettings_PlacementWrittenInTransaction(t *testing.T) {
	s := dbtest.New(t)
	newRedeployApp(t, s, "tiers")
	res, err := s.PatchAppSettings(db.PatchAppSettingsParams{Slug: "tiers", SetPlacement: true, Placement: `{"gpu":2,"cpu":1}`, PlacementTotal: 3, ArmRedeploy: true})
	if err != nil {
		t.Fatal(err)
	}
	app := mustGetApp(t, s, "tiers")
	if app.Replicas != 3 || app.PlacementMap()["gpu"] != 2 || res.RedeploySeq != 1 {
		t.Fatalf("replicas %d placement %q seq %d, want 3, gpu:2, 1", app.Replicas, app.ReplicaPlacement, res.RedeploySeq)
	}
	// Clearing keeps the current replica count on the default tier.
	res, err = s.PatchAppSettings(db.PatchAppSettingsParams{Slug: "tiers", SetPlacement: true, ArmRedeploy: true})
	if err != nil {
		t.Fatal(err)
	}
	app = mustGetApp(t, s, "tiers")
	if app.Replicas != 3 || app.ReplicaPlacement != "" || res.RedeploySeq != 2 {
		t.Fatalf("after clear: replicas %d placement %q seq %d, want 3, \"\", 2", app.Replicas, app.ReplicaPlacement, res.RedeploySeq)
	}
	// Clearing an already-clear placement is not a pool change.
	res, err = s.PatchAppSettings(db.PatchAppSettingsParams{Slug: "tiers", SetPlacement: true, ArmRedeploy: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.RedeploySeq != 0 {
		t.Fatalf("no-op clear armed seq %d", res.RedeploySeq)
	}
}

// redeployStores opens two independent handles on one database (a clustered
// pair), creates a running app with one owed seq, and gives the first handle
// the control-plane lease.
func redeployStores(t *testing.T) (a, b *db.Store, lease db.OwnerLease, seq int64) {
	t.Helper()
	var dsn string
	if os.Getenv("SHINYHUB_TEST_POSTGRES_DSN") != "" {
		a, dsn = dbtest.NewPostgres(t)
	} else {
		dsn = filepath.Join(t.TempDir(), "shared.db")
		dbtest.WriteSQLiteFile(t, dsn)
		var err error
		a, err = db.Open(dsn)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = a.Close() })
	}
	b, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	newRedeployApp(t, a, "web")
	res, err := a.PatchAppSettings(db.PatchAppSettingsParams{Slug: "web", SetReplicas: true, Replicas: 2, ArmRedeploy: true})
	if err != nil {
		t.Fatal(err)
	}
	ok, epoch, err := a.AcquireOwner("node-a", time.Minute)
	if err != nil || !ok {
		t.Fatalf("acquire: %v %v", ok, err)
	}
	return a, b, db.OwnerLease{Instance: "node-a", Epoch: epoch}, res.RedeploySeq
}

func takeOver(t *testing.T, from *db.Store, old db.OwnerLease, to *db.Store) db.OwnerLease {
	t.Helper()
	if err := from.ReleaseOwner(old.Instance, old.Epoch); err != nil {
		t.Fatal(err)
	}
	ok, epoch, err := to.AcquireOwner("node-b", time.Minute)
	if err != nil || !ok {
		t.Fatalf("takeover: %v %v", ok, err)
	}
	if epoch <= old.Epoch {
		t.Fatalf("takeover epoch %d not above %d", epoch, old.Epoch)
	}
	return db.OwnerLease{Instance: "node-b", Epoch: epoch}
}

func owedSeqs(t *testing.T, s *db.Store) map[string]int64 {
	t.Helper()
	owed, err := s.ListOwedRedeploys()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]int64{}
	for _, o := range owed {
		out[o.Slug] = o.Seq
	}
	return out
}

func TestRedeployClaim_ServesEachSeqOnce(t *testing.T) {
	a, _, lease, seq := redeployStores(t)
	ctx := context.Background()
	if ok, err := a.ClaimRedeploy(ctx, &lease, "web", seq); err != nil || !ok {
		t.Fatalf("first claim: %v %v", ok, err)
	}
	if ok, err := a.RecordRedeployOutcome(ctx, &lease, "web", seq, db.RedeployCompleted, ""); err != nil || !ok {
		t.Fatalf("outcome: %v %v", ok, err)
	}
	if ok, err := a.ClaimRedeploy(ctx, &lease, "web", seq); err != nil || ok {
		t.Fatalf("duplicate claim of a served seq: %v %v, want false", ok, err)
	}
	if ok, err := a.RecordRedeployOutcome(ctx, &lease, "web", seq, db.RedeployFailed, "late"); err != nil || ok {
		t.Fatalf("second outcome for one seq: %v %v, want discarded", ok, err)
	}
	app := mustGetApp(t, a, "web")
	if app.LastRedeploy == nil || app.LastRedeploy.Seq != seq || app.LastRedeploy.Outcome != db.RedeployCompleted {
		t.Fatalf("last_redeploy = %+v, want seq %d completed", app.LastRedeploy, seq)
	}
	if time.Since(app.LastRedeploy.At) > time.Minute {
		t.Fatalf("last_redeploy.at = %v, want recent", app.LastRedeploy.At)
	}
	if owed := owedSeqs(t, a); len(owed) != 0 {
		t.Fatalf("owed after outcome: %v", owed)
	}
}

func TestRedeployClaim_SupersededSeqIsNotServed(t *testing.T) {
	a, _, lease, seq := redeployStores(t)
	ctx := context.Background()
	next, err := a.PatchAppSettings(db.PatchAppSettingsParams{Slug: "web", SetReplicas: true, Replicas: 3, ArmRedeploy: true})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := a.ClaimRedeploy(ctx, &lease, "web", seq); err != nil || ok {
		t.Fatalf("claim of superseded seq %d: %v %v, want false", seq, ok, err)
	}
	if ok, err := a.ClaimRedeploy(ctx, &lease, "web", next.RedeploySeq); err != nil || !ok {
		t.Fatalf("claim of newest seq: %v %v", ok, err)
	}
}

func TestRedeployClaim_RetiredOwnerIsFenced(t *testing.T) {
	a, b, old, seq := redeployStores(t)
	ctx := context.Background()
	current := takeOver(t, a, old, b)
	if ok, err := a.ClaimRedeploy(ctx, &old, "web", seq); !errors.Is(err, db.ErrOwnerFenced) || ok {
		t.Fatalf("retired owner claim: %v %v, want ErrOwnerFenced", ok, err)
	}
	if ok, err := b.ClaimRedeploy(ctx, &current, "web", seq); err != nil || !ok {
		t.Fatalf("current owner claim: %v %v", ok, err)
	}
}

// A claimant that loses the lease between claim and outcome must not report:
// its outcome would make the successor see nothing owed for a seq whose cycle
// it never observed.
func TestRedeployOutcome_TakeoverBeforeReclaimDiscardsStaleOutcome(t *testing.T) {
	a, b, old, seq := redeployStores(t)
	ctx := context.Background()
	if ok, err := a.ClaimRedeploy(ctx, &old, "web", seq); err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	current := takeOver(t, a, old, b)
	if ok, err := a.RecordRedeployOutcome(ctx, &old, "web", seq, db.RedeployCompleted, ""); !errors.Is(err, db.ErrOwnerFenced) || ok {
		t.Fatalf("stale outcome: %v %v, want ErrOwnerFenced", ok, err)
	}
	if owed := owedSeqs(t, b); owed["web"] != seq {
		t.Fatalf("owed = %v, want web:%d", owed, seq)
	}
	// The successor's recovery reclaims the dead claimant's seq and reports it.
	if ok, err := b.ClaimRedeploy(ctx, &current, "web", seq); err != nil || !ok {
		t.Fatalf("reclaim: %v %v", ok, err)
	}
	if ok, err := b.RecordRedeployOutcome(ctx, &current, "web", seq, db.RedeployPartial, "1 of 2 replicas failed to start"); err != nil || !ok {
		t.Fatalf("successor outcome: %v %v", ok, err)
	}
	// The outcome is visible through the other handle, as on a clustered peer.
	app := mustGetApp(t, a, "web")
	if app.LastRedeploy == nil || app.LastRedeploy.Outcome != db.RedeployPartial || app.LastRedeploy.Reason != "1 of 2 replicas failed to start" {
		t.Fatalf("peer read last_redeploy = %+v", app.LastRedeploy)
	}
}

// The outcome must come from the claimant: a current owner that never claimed
// the seq (or claimed it at a different epoch) cannot report it.
func TestRedeployOutcome_RequiresTheClaim(t *testing.T) {
	a, b, old, seq := redeployStores(t)
	ctx := context.Background()
	if ok, err := a.ClaimRedeploy(ctx, &old, "web", seq); err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	current := takeOver(t, a, old, b)
	if ok, err := b.RecordRedeployOutcome(ctx, &current, "web", seq, db.RedeployCompleted, ""); err != nil || ok {
		t.Fatalf("outcome without claim: %v %v, want discarded", ok, err)
	}
}

func TestRedeployLease_ExpiredOrNonOwnerIsFenced(t *testing.T) {
	a, _, lease, seq := redeployStores(t)
	ctx := context.Background()
	nonOwner := db.OwnerLease{Instance: "node-a", Epoch: 0}
	if ok, err := a.ClaimRedeploy(ctx, &nonOwner, "web", seq); !errors.Is(err, db.ErrOwnerFenced) || ok {
		t.Fatalf("epoch 0 claim: %v %v", ok, err)
	}
	if _, err := a.DB().Exec(`UPDATE cp_owner SET expires_at='2000-01-01 00:00:00'`); err != nil {
		t.Fatal(err)
	}
	if ok, err := a.ClaimRedeploy(ctx, &lease, "web", seq); !errors.Is(err, db.ErrOwnerFenced) || ok {
		t.Fatalf("expired lease claim: %v %v", ok, err)
	}
	// A nil lease (no elector wired) runs unfenced.
	if ok, err := a.ClaimRedeploy(ctx, nil, "web", seq); err != nil || !ok {
		t.Fatalf("unfenced claim: %v %v", ok, err)
	}
	if ok, err := a.RecordRedeployOutcome(ctx, nil, "web", seq, db.RedeployCompleted, ""); err != nil || !ok {
		t.Fatalf("unfenced outcome: %v %v", ok, err)
	}
}

func TestRestartOutcome_ReplacesBadOutcomeAndServesOwedSeq(t *testing.T) {
	a, _, lease, seq := redeployStores(t)
	ctx := context.Background()
	app := mustGetApp(t, a, "web")
	if ok, err := a.ClaimRedeploy(ctx, &lease, "web", seq); err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	if ok, err := a.RecordRedeployOutcome(ctx, &lease, "web", seq, db.RedeployFailed, "deploy failed: boom"); err != nil || !ok {
		t.Fatalf("outcome: %v %v", ok, err)
	}
	if ok, err := a.RecordBootOutcome(ctx, &lease, app.ID, seq, db.RedeployCompleted, "restart"); err != nil || !ok {
		t.Fatalf("restart outcome: %v %v", ok, err)
	}
	if got := mustGetApp(t, a, "web").LastRedeploy; got.Outcome != db.RedeployCompleted || got.Reason != "restart" {
		t.Fatalf("after restart last_redeploy = %+v, want completed/restart", got)
	}

	// A restart serves a seq that is still owed; the queued redeploy for it
	// then does not cycle the pool a second time.
	next, err := a.PatchAppSettings(db.PatchAppSettingsParams{Slug: "web", SetReplicas: true, Replicas: 3, ArmRedeploy: true})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := a.RecordBootOutcome(ctx, &lease, app.ID, next.RedeploySeq, db.RedeployCompleted, "restart"); err != nil || !ok {
		t.Fatalf("restart serving owed seq: %v %v", ok, err)
	}
	if ok, err := a.ClaimRedeploy(ctx, &lease, "web", next.RedeploySeq); err != nil || ok {
		t.Fatalf("claim after restart served the seq: %v %v, want false", ok, err)
	}
}

func TestRestartOutcome_NewerLaunchedSeqStaysOwed(t *testing.T) {
	a, _, lease, seq := redeployStores(t)
	ctx := context.Background()
	app := mustGetApp(t, a, "web")
	// The restart read seq, then a PATCH launched a newer one before it wrote.
	next, err := a.PatchAppSettings(db.PatchAppSettingsParams{Slug: "web", SetReplicas: true, Replicas: 3, ArmRedeploy: true})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := a.RecordBootOutcome(ctx, &lease, app.ID, seq, db.RedeployCompleted, "restart"); err != nil || ok {
		t.Fatalf("restart outcome for an older seq: %v %v, want discarded", ok, err)
	}
	if owed := owedSeqs(t, a); owed["web"] != next.RedeploySeq {
		t.Fatalf("owed = %v, want web:%d", owed, next.RedeploySeq)
	}
	// Seq 0 (never armed) records nothing.
	if ok, err := a.RecordBootOutcome(ctx, &lease, app.ID, 0, db.RedeployCompleted, "restart"); err != nil || ok {
		t.Fatalf("restart outcome for seq 0: %v %v", ok, err)
	}
}

func TestRestartOutcome_LostLeaseWritesNothing(t *testing.T) {
	a, b, old, seq := redeployStores(t)
	ctx := context.Background()
	app := mustGetApp(t, a, "web")
	takeOver(t, a, old, b)
	if ok, err := a.RecordBootOutcome(ctx, &old, app.ID, seq, db.RedeployCompleted, "restart"); !errors.Is(err, db.ErrOwnerFenced) || ok {
		t.Fatalf("restart outcome with lost lease: %v %v", ok, err)
	}
	if owed := owedSeqs(t, a); owed["web"] != seq {
		t.Fatalf("owed = %v, want web:%d", owed, seq)
	}
}

func TestRedeployOutcome_DeleteAndRecreateResetsSeq(t *testing.T) {
	s := dbtest.New(t)
	app := newRedeployApp(t, s, "again")
	if _, err := s.PatchAppSettings(db.PatchAppSettingsParams{Slug: "again", SetReplicas: true, Replicas: 2, ArmRedeploy: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteApp(app.Slug); err != nil {
		t.Fatal(err)
	}
	fresh := mustCreateApp(t, s, "again", app.OwnerID)
	if fresh.RedeploySeqLaunched != 0 || fresh.LastRedeploy != nil {
		t.Fatalf("recreated app carries seq %d / %+v", fresh.RedeploySeqLaunched, fresh.LastRedeploy)
	}
	if owed := owedSeqs(t, s); len(owed) != 0 {
		t.Fatalf("owed after delete: %v", owed)
	}
}

// A structural seq whose outcome did not apply the stored settings to the pool
// (failed or skipped) leaves the full cycle owed, so the next replica-only seq
// cycles the pool instead of resizing a pool still on the old settings. An
// outcome that booted the pool on the new settings (completed or partial, from
// a redeploy or a restart) serves it.
func TestRedeployFullCycle_OwedUntilApplied(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name    string
		boot    bool
		outcome string
		owed    bool
	}{
		{"redeploy completed", false, db.RedeployCompleted, false},
		{"redeploy partial", false, db.RedeployPartial, false},
		{"redeploy failed", false, db.RedeployFailed, true},
		{"redeploy skipped", false, db.RedeploySkipped, true},
		{"restart completed", true, db.RedeployCompleted, false},
		{"restart partial", true, db.RedeployPartial, false},
		{"restart failed", true, db.RedeployFailed, true},
		{"deploy kept stopped", true, db.RedeploySkipped, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := dbtest.New(t)
			app := newRedeployApp(t, s, "owe")
			res, err := s.PatchAppSettings(db.PatchAppSettingsParams{Slug: "owe", SetMemoryLimitMB: true, MemoryLimitMB: intp(256), ArmRedeploy: true})
			if err != nil {
				t.Fatal(err)
			}
			if tc.boot {
				if ok, err := s.RecordBootOutcome(ctx, nil, app.ID, res.RedeploySeq, tc.outcome, "boot"); err != nil || !ok {
					t.Fatalf("boot outcome: %v %v", ok, err)
				}
			} else {
				if ok, err := s.ClaimRedeploy(ctx, nil, "owe", res.RedeploySeq); err != nil || !ok {
					t.Fatalf("claim: %v %v", ok, err)
				}
				if ok, err := s.RecordRedeployOutcome(ctx, nil, "owe", res.RedeploySeq, tc.outcome, "x"); err != nil || !ok {
					t.Fatalf("outcome: %v %v", ok, err)
				}
			}
			next, err := s.PatchAppSettings(db.PatchAppSettingsParams{Slug: "owe", SetReplicas: true, Replicas: 2, ArmRedeploy: true})
			if err != nil {
				t.Fatal(err)
			}
			full, err := s.RedeployNeedsFullCycle(ctx, "owe")
			if err != nil {
				t.Fatal(err)
			}
			if full != tc.owed {
				t.Fatalf("replica-only seq after a %s structural seq: needs full cycle = %v, want %v", tc.outcome, full, tc.owed)
			}
			if !tc.owed {
				return
			}
			// Serving the owed cycle settles it: the following replica-only seq resizes.
			if ok, err := s.ClaimRedeploy(ctx, nil, "owe", next.RedeploySeq); err != nil || !ok {
				t.Fatalf("claim: %v %v", ok, err)
			}
			if ok, err := s.RecordRedeployOutcome(ctx, nil, "owe", next.RedeploySeq, db.RedeployCompleted, ""); err != nil || !ok {
				t.Fatalf("outcome: %v %v", ok, err)
			}
			if _, err := s.PatchAppSettings(db.PatchAppSettingsParams{Slug: "owe", SetReplicas: true, Replicas: 3, ArmRedeploy: true}); err != nil {
				t.Fatal(err)
			}
			if full, err := s.RedeployNeedsFullCycle(ctx, "owe"); err != nil || full {
				t.Fatalf("after the owed cycle completed: needs full cycle = %v %v, want false", full, err)
			}
		})
	}
}

// A restart that boots the pool after a failed structural redeploy applies
// every stored setting, so it settles the owed cycle carried past that failure.
func TestRedeployFullCycle_RestartSettlesCarriedCycle(t *testing.T) {
	ctx := context.Background()
	s := dbtest.New(t)
	app := newRedeployApp(t, s, "carry")
	res, err := s.PatchAppSettings(db.PatchAppSettingsParams{Slug: "carry", SetMemoryLimitMB: true, MemoryLimitMB: intp(256), ArmRedeploy: true})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := s.ClaimRedeploy(ctx, nil, "carry", res.RedeploySeq); err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	if ok, err := s.RecordRedeployOutcome(ctx, nil, "carry", res.RedeploySeq, db.RedeployFailed, "deploy failed"); err != nil || !ok {
		t.Fatalf("outcome: %v %v", ok, err)
	}
	if ok, err := s.RecordBootOutcome(ctx, nil, app.ID, res.RedeploySeq, db.RedeployCompleted, "restart"); err != nil || !ok {
		t.Fatalf("restart outcome: %v %v", ok, err)
	}
	if _, err := s.PatchAppSettings(db.PatchAppSettingsParams{Slug: "carry", SetReplicas: true, Replicas: 2, ArmRedeploy: true}); err != nil {
		t.Fatal(err)
	}
	if full, err := s.RedeployNeedsFullCycle(ctx, "carry"); err != nil || full {
		t.Fatalf("replica-only seq after a restart: needs full cycle = %v %v, want false", full, err)
	}
}
