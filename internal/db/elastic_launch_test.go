package db_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

func TestElasticLaunchBindingAndCommandStates(t *testing.T) {
	a, b, app, dep, owner := elasticStores(t)
	ctx := context.Background()
	r, err := a.ReserveElasticSession(ctx, owner, app.ID, dep, "client")
	if err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("a", 64)
	check := func(action string, want error) {
		t.Helper()
		err := b.AuthorizeElasticCommand(ctx, owner, r.ID, "node-a", digest, action)
		if !errors.Is(err, want) {
			t.Fatalf("%s: got %v, want %v", action, err, want)
		}
	}
	for _, action := range []string{"prepare", "ack", "stop"} {
		check(action, db.ErrElasticConflict)
	}
	if err := a.BindElasticLaunch(ctx, owner, r.ID, "node-a", digest); err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"launching", "ready"} {
		if err := a.AdvanceElasticReservation(ctx, owner, r.ID, state); err != nil {
			t.Fatal(err)
		}
		if err := b.BindElasticLaunch(ctx, owner, r.ID, "node-a", digest); err != nil {
			t.Fatalf("retry %s: %v", state, err)
		}
		check("prepare", nil)
		check("ack", nil)
		check("stop", db.ErrElasticConflict)
		check("unknown", db.ErrElasticConflict)
	}
	for _, binding := range [][2]string{{"node-b", digest}, {"node-a", strings.Repeat("b", 64)}, {"node-a", "bad"}, {"", digest}, {"node-a", strings.Repeat("A", 64)}} {
		if err := a.BindElasticLaunch(ctx, owner, r.ID, binding[0], binding[1]); !errors.Is(err, db.ErrElasticConflict) {
			t.Fatalf("changed binding: %v", err)
		}
		if err := b.AuthorizeElasticCommand(ctx, owner, r.ID, binding[0], binding[1], "ack"); !errors.Is(err, db.ErrElasticConflict) {
			t.Fatalf("changed command binding: %v", err)
		}
	}
	if err := a.StopElasticReservation(ctx, owner, r.ID); err != nil {
		t.Fatal(err)
	}
	check("prepare", db.ErrElasticConflict)
	check("ack", db.ErrElasticConflict)
	check("stop", nil)
	if err := a.BindElasticLaunch(ctx, owner, r.ID, "node-a", digest); !errors.Is(err, db.ErrElasticConflict) {
		t.Fatalf("bind while stopping: %v", err)
	}
	if _, err := b.ReserveElasticSession(ctx, owner, app.ID, dep, "another"); !errors.Is(err, db.ErrElasticCapacity) {
		t.Fatalf("command authorization freed capacity: %v", err)
	}
	if err := a.ConfirmElasticReservationStopped(ctx, owner, r.ID); err != nil {
		t.Fatal(err)
	}
	check("stop", nil)
	check("ack", db.ErrElasticConflict)
	check("prepare", db.ErrElasticConflict)
}

func TestElasticLaunchTakeoverAndExpiry(t *testing.T) {
	a, b, app, dep, old := elasticStores(t)
	ctx := context.Background()
	r, err := a.ReserveElasticSession(ctx, old, app.ID, dep, "client")
	if err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("a", 64)
	if err := a.BindElasticLaunch(ctx, old, r.ID, "node-a", digest); err != nil {
		t.Fatal(err)
	}
	if err := a.ReleaseOwner(old.Instance, old.Epoch); err != nil {
		t.Fatal(err)
	}
	ok, epoch, err := b.AcquireOwner("controller-b", time.Minute)
	if err != nil || !ok {
		t.Fatalf("takeover: %v %v", ok, err)
	}
	owner := db.ElasticOwner{Instance: "controller-b", Epoch: epoch}
	if err := a.BindElasticLaunch(ctx, old, r.ID, "node-a", digest); !errors.Is(err, db.ErrElasticFenced) {
		t.Fatalf("stale bind: %v", err)
	}
	for _, candidate := range []db.ElasticOwner{old, owner} {
		if err := a.AuthorizeElasticCommand(ctx, candidate, r.ID, "node-a", digest, "ack"); !errors.Is(err, db.ErrElasticFenced) {
			t.Fatalf("unclaimed/stale acknowledgement: %v", err)
		}
	}
	if err := b.StopElasticReservation(ctx, owner, r.ID); err != nil {
		t.Fatal(err)
	}
	if err := b.AuthorizeElasticCommand(ctx, owner, r.ID, "node-a", digest, "stop"); err != nil {
		t.Fatal(err)
	}
	if err := b.AuthorizeElasticCommand(ctx, owner, r.ID, "node-b", digest, "stop"); !errors.Is(err, db.ErrElasticConflict) {
		t.Fatalf("takeover changed node: %v", err)
	}
	if _, err := a.DB().Exec(`UPDATE cp_owner SET expires_at='2000-01-01 00:00:00'`); err != nil {
		t.Fatal(err)
	}
	if err := b.AuthorizeElasticCommand(ctx, owner, r.ID, "node-a", digest, "stop"); !errors.Is(err, db.ErrElasticFenced) {
		t.Fatalf("expired stop: %v", err)
	}
}

func TestElasticLaunchConcurrentBinding(t *testing.T) {
	a, b, app, dep, owner := elasticStores(t)
	ctx := context.Background()
	r, err := a.ReserveElasticSession(ctx, owner, app.ID, dep, "client")
	if err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("a", 64)
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make(chan string, 2)
	for i, node := range []string{"node-a", "node-b"} {
		wg.Add(1)
		go func(store *db.Store, node string) {
			defer wg.Done()
			<-start
			if err := store.BindElasticLaunch(ctx, owner, r.ID, node, digest); err == nil {
				results <- node
			} else if !errors.Is(err, db.ErrElasticConflict) {
				t.Errorf("bind: %v", err)
			}
		}([]*db.Store{a, b}[i], node)
	}
	close(start)
	wg.Wait()
	close(results)
	var winner string
	for node := range results {
		if winner != "" {
			t.Fatal("two workers admitted")
		}
		winner = node
	}
	if winner == "" {
		t.Fatal("no worker admitted")
	}
	if err := b.BindElasticLaunch(ctx, owner, r.ID, winner, digest); err != nil {
		t.Fatalf("durable retry: %v", err)
	}
}

func TestElasticLaunchCannotBindAlreadyUnboundLaunch(t *testing.T) {
	a, _, app, dep, owner := elasticStores(t)
	ctx := context.Background()
	r, err := a.ReserveElasticSession(ctx, owner, app.ID, dep, "client")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.AdvanceElasticReservation(ctx, owner, r.ID, "launching"); err != nil {
		t.Fatal(err)
	}
	if err := a.BindElasticLaunch(ctx, owner, r.ID, "node-a", strings.Repeat("a", 64)); !errors.Is(err, db.ErrElasticConflict) {
		t.Fatalf("bound unknown launch: %v", err)
	}
}

func TestElasticLaunchRejectsChangedAppIntent(t *testing.T) {
	for _, change := range []string{"stopped", "deployment", "isolation", "capacity"} {
		for _, phase := range []string{"reserved", "launching", "ready"} {
			t.Run(change+"/"+phase, func(t *testing.T) {
				a, b, app, dep, owner := elasticStores(t)
				ctx := context.Background()
				r, err := a.ReserveElasticSession(ctx, owner, app.ID, dep, "client")
				if err != nil {
					t.Fatal(err)
				}
				digest := strings.Repeat("a", 64)
				if phase != "reserved" {
					if err := a.BindElasticLaunch(ctx, owner, r.ID, "node-a", digest); err != nil {
						t.Fatal(err)
					}
					if phase == "ready" {
						if err := a.AdvanceElasticReservation(ctx, owner, r.ID, "ready"); err != nil {
							t.Fatal(err)
						}
					}
				}
				switch change {
				case "deployment":
					next, err := b.BeginDeployment(app.ID, "replacement", t.TempDir())
					if err != nil {
						t.Fatal(err)
					}
					if err := b.PromoteDeployment(next.ID); err != nil {
						t.Fatal(err)
					}
				case "stopped":
					_, err = b.DB().Exec(`UPDATE apps SET status = 'stopped' WHERE id = ?`, app.ID)
				case "isolation":
					_, err = b.DB().Exec(`UPDATE apps SET worker_isolation = 'shared' WHERE id = ?`, app.ID)
				case "capacity":
					_, err = b.DB().Exec(`UPDATE apps SET worker_max_workers = 0 WHERE id = ?`, app.ID)
				}
				if err != nil {
					t.Fatal(err)
				}
				if err := a.BindElasticLaunch(ctx, owner, r.ID, "node-a", digest); !errors.Is(err, db.ErrElasticConflict) {
					t.Fatalf("stale bind/retry: %v", err)
				}
				for _, action := range []string{"prepare", "ack"} {
					if err := a.AuthorizeElasticCommand(ctx, owner, r.ID, "node-a", digest, action); !errors.Is(err, db.ErrElasticConflict) {
						t.Fatalf("stale %s: %v", action, err)
					}
				}
				current, err := b.GetElasticReservation(ctx, r.ID)
				if err != nil || current.State != phase {
					t.Fatalf("rejection changed reservation: %+v, %v", current, err)
				}
				if err := a.StopElasticReservation(ctx, owner, r.ID); err != nil {
					t.Fatalf("stop stale launch: %v", err)
				}
				if phase != "reserved" {
					if err := b.AuthorizeElasticCommand(ctx, owner, r.ID, "node-a", digest, "stop"); err != nil {
						t.Fatalf("app mutation blocked cleanup: %v", err)
					}
				}
			})
		}
	}
}

func TestElasticLaunchConfirmedStopRetryRemainsFenced(t *testing.T) {
	a, b, app, dep, owner := elasticStores(t)
	ctx := context.Background()
	r, err := a.ReserveElasticSession(ctx, owner, app.ID, dep, "client")
	if err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("a", 64)
	if err := a.BindElasticLaunch(ctx, owner, r.ID, "node-a", digest); err != nil {
		t.Fatal(err)
	}
	if err := a.StopElasticReservation(ctx, owner, r.ID); err != nil {
		t.Fatal(err)
	}
	if err := a.ConfirmElasticReservationStopped(ctx, owner, r.ID); err != nil {
		t.Fatal(err)
	}
	if err := b.StopElasticReservation(ctx, owner, r.ID); err != nil {
		t.Fatalf("confirmed stop retry: %v", err)
	}
	current, err := b.GetElasticReservation(ctx, r.ID)
	if err != nil || current.State != "stopped" {
		t.Fatalf("retry reopened reservation: %+v, %v", current, err)
	}
	if err := a.ReleaseOwner(owner.Instance, owner.Epoch); err != nil {
		t.Fatal(err)
	}
	ok, epoch, err := b.AcquireOwner("controller-b", time.Minute)
	if err != nil || !ok {
		t.Fatalf("takeover: %v %v", ok, err)
	}
	if err := a.StopElasticReservation(ctx, owner, r.ID); !errors.Is(err, db.ErrElasticFenced) {
		t.Fatalf("stale confirmed-stop retry: %v", err)
	}
	if err := b.StopElasticReservation(ctx, db.ElasticOwner{Instance: "controller-b", Epoch: epoch}, r.ID); !errors.Is(err, db.ErrElasticConflict) {
		t.Fatalf("new owner rewrote terminal identity: %v", err)
	}
}

func TestElasticLaunchPostgresLeaseExpiresDuringAppLock(t *testing.T) {
	if os.Getenv("SHINYHUB_TEST_POSTGRES_DSN") == "" {
		t.Skip("requires PostgreSQL row locks and live database clock")
	}
	a, dsn := dbtest.NewPostgres(t)
	observer, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	u := mustCreateUser(t, a, "owner", "admin")
	app := mustCreateApp(t, a, "lock-expiry", u.ID)
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
	ok, epoch, err := a.AcquireOwner("lock-controller", time.Minute)
	if err != nil || !ok {
		t.Fatalf("acquire: %v %v", ok, err)
	}
	owner := db.ElasticOwner{Instance: "lock-controller", Epoch: epoch}
	r, err := a.ReserveElasticSession(ctx, owner, app.ID, dep.ID, "client")
	if err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("a", 64)
	if err := a.BindElasticLaunch(ctx, owner, r.ID, "node-a", digest); err != nil {
		t.Fatal(err)
	}
	holder, err := observer.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback() //nolint:errcheck
	var holderPID int
	if err := holder.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&holderPID); err != nil {
		t.Fatal(err)
	}
	if _, err := holder.ExecContext(ctx, `UPDATE apps SET id=id WHERE id=$1`, app.ID); err != nil {
		t.Fatal(err)
	}
	// Shorten only after setup, before authorization takes the owner lock.
	if _, err := observer.ExecContext(ctx, `UPDATE cp_owner SET expires_at=clock_timestamp()+INTERVAL '2 seconds'`); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		result <- a.AuthorizeElasticCommand(ctx, owner, r.ID, "node-a", digest, "ack")
	}()
	awaitCondition := func(query string, args ...any) {
		t.Helper()
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			var reached bool
			if err := observer.QueryRowContext(ctx, query, args...).Scan(&reached); err != nil {
				t.Fatal(err)
			}
			if reached {
				return
			}
			select {
			case err := <-result:
				t.Fatalf("authorization returned before app lock released: %v", err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-ticker.C:
			}
		}
	}
	// Observe the actual row-lock wait rather than assuming the goroutine ran.
	awaitCondition(`SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE wait_event_type='Lock' AND $1=ANY(pg_blocking_pids(pid)))`, holderPID)
	awaitCondition(`SELECT expires_at <= clock_timestamp() FROM cp_owner WHERE instance_id=$1`, owner.Instance)
	if err := holder.Rollback(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, db.ErrElasticFenced) {
			t.Fatalf("grant after lease expired during app lock: %v", err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}
