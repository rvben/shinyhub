package db_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

// Independent connection pools are essential: one in-memory SQLite connection
// would hide a missing transaction lock. PostgreSQL CI exercises the same cases.
func elasticStores(t *testing.T) (*db.Store, *db.Store, *db.App, int64, db.ElasticOwner) {
	t.Helper()
	var a *db.Store
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
	u := mustCreateUser(t, a, "owner", "admin")
	app := mustCreateApp(t, a, "isolated", u.ID)
	dep, err := a.BeginDeployment(app.ID, "one", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := a.PromoteDeployment(dep.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.DB().Exec(`UPDATE apps SET worker_isolation='per_session', worker_max_workers=1, status='running' WHERE id=?`, app.ID); err != nil {
		t.Fatal(err)
	}
	ok, epoch, err := a.AcquireOwner("controller-a", time.Minute)
	if err != nil || !ok {
		t.Fatalf("acquire: %v %v", ok, err)
	}
	return a, b, app, dep.ID, db.ElasticOwner{Instance: "controller-a", Epoch: epoch}
}

func TestElasticReservationsConcurrentAdmission(t *testing.T) {
	for _, sameClient := range []bool{false, true} {
		t.Run(fmt.Sprintf("same_client=%t", sameClient), func(t *testing.T) {
			a, b, app, dep, owner := elasticStores(t)
			var wg sync.WaitGroup
			start := make(chan struct{})
			ids := make(chan string, 24)
			for i := 0; i < 24; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					<-start
					client := fmt.Sprintf("client-%d", i)
					if sameClient {
						client = "same-client"
					}
					store := []*db.Store{a, b}[i%2]
					r, err := store.ReserveElasticSession(context.Background(), owner, app.ID, dep, client)
					if errors.Is(err, db.ErrElasticCapacity) && !sameClient {
						return
					}
					if err != nil {
						t.Errorf("reserve: %v", err)
						return
					}
					ids <- r.ID
				}(i)
			}
			close(start)
			wg.Wait()
			close(ids)
			unique := map[string]bool{}
			count := 0
			for id := range ids {
				unique[id] = true
				count++
			}
			want := 1
			if sameClient {
				want = 24
			}
			if len(unique) != 1 || count != want {
				t.Fatalf("admitted %d requests into %d workers, want %d into one", count, len(unique), want)
			}
		})
	}
}

func TestElasticReservationsTakeoverRetainsCapacity(t *testing.T) {
	a, b, app, dep, old := elasticStores(t)
	ctx := context.Background()
	r, err := a.ReserveElasticSession(ctx, old, app.ID, dep, "client-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.AdvanceElasticReservation(ctx, old, r.ID, "launching"); err != nil {
		t.Fatal(err)
	}
	if err := a.ReleaseOwner(old.Instance, old.Epoch); err != nil {
		t.Fatal(err)
	}
	ok, epoch, err := b.AcquireOwner("controller-b", time.Minute)
	if err != nil || !ok {
		t.Fatalf("takeover: %v %v", ok, err)
	}
	current := db.ElasticOwner{Instance: "controller-b", Epoch: epoch}
	if err := a.AdvanceElasticReservation(ctx, old, r.ID, "ready"); !errors.Is(err, db.ErrElasticFenced) {
		t.Fatalf("stale launch acknowledgement: %v", err)
	}
	if _, err := b.ReserveElasticSession(ctx, current, app.ID, dep, "client-a"); !errors.Is(err, db.ErrElasticConflict) {
		t.Fatalf("unreconciled client rebound: %v", err)
	}
	if _, err := b.ReserveElasticSession(ctx, current, app.ID, dep, "client-b"); !errors.Is(err, db.ErrElasticCapacity) {
		t.Fatalf("takeover freed capacity: %v", err)
	}
	if err := b.ConfirmElasticReservationStopped(ctx, current, r.ID); !errors.Is(err, db.ErrElasticFenced) {
		t.Fatalf("unclaimed cleanup accepted: %v", err)
	}
	if err := b.StopElasticReservation(ctx, current, r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := b.ReserveElasticSession(ctx, current, app.ID, dep, "client-b"); !errors.Is(err, db.ErrElasticCapacity) {
		t.Fatalf("stop intent freed capacity: %v", err)
	}
	if err := a.ConfirmElasticReservationStopped(ctx, old, r.ID); !errors.Is(err, db.ErrElasticFenced) {
		t.Fatalf("stale cleanup accepted: %v", err)
	}
	if err := b.ConfirmElasticReservationStopped(ctx, current, r.ID); err != nil {
		t.Fatal(err)
	}
	if err := b.ConfirmElasticReservationStopped(ctx, current, r.ID); err != nil {
		t.Fatalf("cleanup retry: %v", err)
	}
	replacement, err := b.ReserveElasticSession(ctx, current, app.ID, dep, "client-a")
	if err != nil {
		t.Fatal(err)
	}
	if replacement.ID == r.ID || replacement.Slot <= r.Slot {
		t.Fatal("replacement reused stale routing identity")
	}
}

func TestElasticReservationsRejectExpiredOwnerAndInvalidProgress(t *testing.T) {
	a, _, app, dep, owner := elasticStores(t)
	ctx := context.Background()
	r, err := a.ReserveElasticSession(ctx, owner, app.ID, dep, "client")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.AdvanceElasticReservation(ctx, owner, r.ID, "ready"); !errors.Is(err, db.ErrElasticConflict) {
		t.Fatalf("skipped launch: %v", err)
	}
	for _, state := range []string{"launching", "launching", "ready", "ready"} {
		if err := a.AdvanceElasticReservation(ctx, owner, r.ID, state); err != nil {
			t.Fatalf("progress/retry %s: %v", state, err)
		}
	}
	if err := a.AdvanceElasticReservation(ctx, owner, r.ID, "launching"); !errors.Is(err, db.ErrElasticConflict) {
		t.Fatalf("backwards transition: %v", err)
	}
	if _, err := a.ReserveElasticSession(ctx, owner, app.ID, dep+1, "client"); !errors.Is(err, db.ErrElasticConflict) {
		t.Fatalf("wrong deployment: %v", err)
	}
	if _, err := a.DB().Exec(`UPDATE cp_owner SET expires_at='2000-01-01 00:00:00'`); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ReserveElasticSession(ctx, owner, app.ID, dep, "client"); !errors.Is(err, db.ErrElasticFenced) {
		t.Fatalf("expired retry: %v", err)
	}
	if err := a.StopElasticReservation(ctx, owner, r.ID); !errors.Is(err, db.ErrElasticFenced) {
		t.Fatalf("expired cleanup: %v", err)
	}
}

func TestElasticReservationsOldGenerationStillConsumesCapacity(t *testing.T) {
	a, b, app, dep, owner := elasticStores(t)
	ctx := context.Background()
	if _, err := a.ReserveElasticSession(ctx, owner, app.ID, dep, "old-client"); err != nil {
		t.Fatal(err)
	}
	next, err := a.BeginDeployment(app.ID, "two", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := a.PromoteDeployment(next.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := b.ReserveElasticSession(ctx, owner, app.ID, next.ID, "new-client"); !errors.Is(err, db.ErrElasticCapacity) {
		t.Fatalf("old generation capacity forgotten: %v", err)
	}
	if _, err := b.ReserveElasticSession(ctx, owner, app.ID, next.ID, "old-client"); !errors.Is(err, db.ErrElasticConflict) {
		t.Fatalf("old client rebound to new code: %v", err)
	}
}
