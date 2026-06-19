package proxy_test

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
	"github.com/rvben/shinyhub/internal/proxy"
	"github.com/rvben/shinyhub/internal/worker"
)

// --- test doubles ---

// staticSource is a RoutableSource whose rows are set once and returned on
// every call.
type staticSource struct {
	rows []db.RoutableReplica
	err  error
}

func (s *staticSource) ListRoutableReplicas() ([]db.RoutableReplica, error) {
	return s.rows, s.err
}

// swappableSource is a RoutableSource whose rows can be swapped between calls.
type swappableSource struct {
	ptr atomic.Pointer[[]db.RoutableReplica]
}

func (s *swappableSource) ListRoutableReplicas() ([]db.RoutableReplica, error) {
	if p := s.ptr.Load(); p != nil {
		return *p, nil
	}
	return nil, nil
}

func (s *swappableSource) set(rows []db.RoutableReplica) {
	s.ptr.Store(&rows)
}

// noopTransport returns nil for every replica (fargate/native default).
type noopTransport struct{}

func (noopTransport) TransportForReplica(_ *db.Replica) (http.RoundTripper, error) {
	return nil, nil
}

// recordingTransport records every replica it was asked for and returns a
// distinct *http.Transport per worker_id so tests can assert identity.
type recordingTransport struct {
	seen   []*db.Replica
	custom http.RoundTripper // returned for every call (nil = default)
}

func (r *recordingTransport) TransportForReplica(rep *db.Replica) (http.RoundTripper, error) {
	r.seen = append(r.seen, rep)
	return r.custom, nil
}

// makeReplica returns a RoutableReplica with sensible defaults.
func makeReplica(slug string, appID int64, idx int, url string, depID int64) db.RoutableReplica {
	d := depID
	return db.RoutableReplica{
		Slug:                  slug,
		AppMaxSessionsPerRepl: 0,
		Replica: &db.Replica{
			AppID:        appID,
			Index:        idx,
			Status:       db.ReplicaStatusRunning,
			EndpointURL:  url,
			DeploymentID: &d,
			DesiredState: "running",
			Provider:     "fargate",
		},
	}
}

// --- tests ---

// TestPoolSyncer_BuildsPoolFromRows asserts that a standby with an empty pool
// can serve an app after one sync tick.
func TestPoolSyncer_BuildsPoolFromRows(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("synced"))
	}))
	defer backend.Close()

	rr := makeReplica("my-app", 1, 0, backend.URL, 42)
	src := &staticSource{rows: []db.RoutableReplica{rr}}

	prx := proxy.New()
	prx.MarkSynced() // normally done after first sync, but mark now to avoid readyz interference
	syncer := proxy.NewPoolSyncer(prx, src, noopTransport{}, slog.Default(), true)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Run a single sync.
	syncer.RunOnce(ctx)

	// The proxy should now route requests for "my-app".
	req := httptest.NewRequest("GET", "/app/my-app/", nil)
	rec := httptest.NewRecorder()
	prx.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "synced" {
		t.Errorf("body = %q, want synced", rec.Body.String())
	}
}

// TestPoolSyncer_DegradedAppIncluded guards the invariant that a degraded
// parent app (the watcher marks an app degraded when a replica crashes) with
// surviving running replicas stays routable, because the syncer sources from
// replica.status, not app.status.
func TestPoolSyncer_DegradedAppIncluded(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("degraded-but-routable"))
	}))
	defer backend.Close()

	// The app row has status='degraded' in the DB, but the replica row has
	// status='running'. ListRoutableReplicas returns it (that's tested in the DB
	// layer). Here we verify that when such a row is present, the syncer routes
	// to it correctly.
	rr := makeReplica("degraded-app", 10, 0, backend.URL, 1)
	src := &staticSource{rows: []db.RoutableReplica{rr}}

	prx := proxy.New()
	syncer := proxy.NewPoolSyncer(prx, src, noopTransport{}, slog.Default(), true)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	syncer.RunOnce(ctx)

	req := httptest.NewRequest("GET", "/app/degraded-app/", nil)
	rec := httptest.NewRecorder()
	prx.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if rec.Body.String() != "degraded-but-routable" {
		t.Errorf("body = %q, want degraded-but-routable", rec.Body.String())
	}
}

// TestPoolSyncer_DiffPreservesWSReady asserts that a sync for an unchanged slot
// (same endpoint_url + deployment_id) does NOT re-register the backend and thus
// preserves the wsReady flag.
func TestPoolSyncer_DiffPreservesWSReady(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer backend.Close()

	rr := makeReplica("ready-app", 1, 0, backend.URL, 99)
	src := &staticSource{rows: []db.RoutableReplica{rr}}

	prx := proxy.New()
	syncer := proxy.NewPoolSyncer(prx, src, noopTransport{}, slog.Default(), true)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// First sync registers the backend.
	syncer.RunOnce(ctx)

	// Manually mark wsReady (simulating a successful WS handshake).
	prx.MarkWSReady("ready-app")
	if !prx.IsWSReady("ready-app") {
		t.Fatal("expected wsReady after MarkWSReady")
	}

	// Second sync with identical rows must NOT clear wsReady.
	syncer.RunOnce(ctx)

	if !prx.IsWSReady("ready-app") {
		t.Fatal("second sync with unchanged rows must not clear wsReady (diff-based invariant)")
	}

	// A sync with a CHANGED endpoint clears wsReady.
	backend2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok2"))
	}))
	defer backend2.Close()

	rr2 := makeReplica("ready-app", 1, 0, backend2.URL, 99)
	src.rows = []db.RoutableReplica{rr2}
	syncer.RunOnce(ctx)

	if prx.IsWSReady("ready-app") {
		t.Fatal("sync with changed endpoint must clear wsReady")
	}
}

// TestPoolSyncer_DeregistersMissingSlot asserts that a slot whose replica no
// longer appears in the routable set is deregistered on the next sync.
func TestPoolSyncer_DeregistersMissingSlot(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer backend.Close()

	rr := makeReplica("going-away", 1, 0, backend.URL, 1)
	var src swappableSource
	src.set([]db.RoutableReplica{rr})

	prx := proxy.New()
	syncer := proxy.NewPoolSyncer(prx, &src, noopTransport{}, slog.Default(), true)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	syncer.RunOnce(ctx)
	if prx.ReplicaTargetURL("going-away", 0) == "" {
		t.Fatal("replica should be registered after first sync")
	}

	// Simulate the replica going lost: remove it from the routable set.
	src.set(nil)
	syncer.RunOnce(ctx)

	if prx.ReplicaTargetURL("going-away", 0) != "" {
		t.Fatal("replica should be deregistered after it leaves the routable set")
	}
}

// TestPoolSyncer_DrainState asserts desired_state='draining' marks the slot
// draining, and reverting to 'running' undrains it.
func TestPoolSyncer_DrainState(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer backend.Close()

	depID := int64(1)
	makeRow := func(desired string) db.RoutableReplica {
		return db.RoutableReplica{
			Slug:                  "drain-app",
			AppMaxSessionsPerRepl: 0,
			Replica: &db.Replica{
				AppID:        1,
				Index:        0,
				Status:       db.ReplicaStatusRunning,
				EndpointURL:  backend.URL,
				DeploymentID: &depID,
				DesiredState: desired,
				Provider:     "fargate",
			},
		}
	}

	var src swappableSource
	src.set([]db.RoutableReplica{makeRow("running")})

	prx := proxy.New()
	syncer := proxy.NewPoolSyncer(prx, &src, noopTransport{}, slog.Default(), true)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	syncer.RunOnce(ctx)
	if prx.IsDraining("drain-app", 0) {
		t.Fatal("slot must not be draining when desired_state=running")
	}

	// Switch to desired_state='draining'.
	src.set([]db.RoutableReplica{makeRow("draining")})
	syncer.RunOnce(ctx)
	if !prx.IsDraining("drain-app", 0) {
		t.Fatal("slot must be draining after desired_state=draining")
	}

	// Revert to 'running'.
	src.set([]db.RoutableReplica{makeRow("running")})
	syncer.RunOnce(ctx)
	if prx.IsDraining("drain-app", 0) {
		t.Fatal("slot must be undraining after desired_state reverts to running")
	}
}

// TestPoolSyncer_DeploymentIDAndTransport asserts that the synced backend
// carries the row's deployment_id in the sticky-cookie value, and that a
// remote_docker row uses the transport provided by the builder.
func TestPoolSyncer_DeploymentIDAndTransport(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer backend.Close()

	customTransport := &http.Transport{}
	builder := &recordingTransport{custom: customTransport}

	depID := int64(777)
	rr := db.RoutableReplica{
		Slug: "transport-app",
		Replica: &db.Replica{
			AppID:        1,
			Index:        0,
			Status:       db.ReplicaStatusRunning,
			EndpointURL:  backend.URL,
			DeploymentID: &depID,
			DesiredState: "running",
			Provider:     "remote_docker",
			WorkerID:     "worker-1",
		},
	}
	src := &staticSource{rows: []db.RoutableReplica{rr}}

	prx := proxy.New()
	syncer := proxy.NewPoolSyncer(prx, src, builder, slog.Default(), true)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	syncer.RunOnce(ctx)

	// The builder must have been called.
	if len(builder.seen) == 0 {
		t.Fatal("TransportForReplica was not called")
	}
	if builder.seen[0].WorkerID != "worker-1" {
		t.Errorf("TransportForReplica called with wrong worker_id: %q", builder.seen[0].WorkerID)
	}

	// The deployment ID must be recorded in the pool so sticky-cookie validation
	// works. ReplicaDeploymentID is the introspection helper added for the syncer.
	if got := prx.ReplicaDeploymentID("transport-app", 0); got != 777 {
		t.Errorf("deployment_id = %d, want 777", got)
	}
}

// TestPoolSyncer_OnMissSync_Clustered asserts that a request to an unknown
// slug triggers SyncSlug and, if the sync populates the pool, the request is
// served (rather than the loading page).
func TestPoolSyncer_OnMissSync_Clustered(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("on-miss"))
	}))
	defer backend.Close()

	rr := makeReplica("miss-app", 1, 0, backend.URL, 1)
	src := &staticSource{rows: []db.RoutableReplica{rr}}

	prx := proxy.New()
	syncer := proxy.NewPoolSyncer(prx, src, noopTransport{}, slog.Default(), true)

	// Wire on-miss sync (clustered-only).
	prx.SetOnMissSync(func(slug string) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		syncer.SyncSlug(ctx, slug)
	})

	req := httptest.NewRequest("GET", "/app/miss-app/", nil)
	rec := httptest.NewRecorder()
	prx.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if rec.Body.String() != "on-miss" {
		t.Errorf("body = %q, want on-miss", rec.Body.String())
	}
}

// TestPoolSyncer_SyncedOnce asserts that SyncedOnce() becomes true after the
// first successful sync.
func TestPoolSyncer_SyncedOnce(t *testing.T) {
	prx := proxy.New()
	if prx.SyncedOnce() {
		t.Fatal("SyncedOnce must be false before first sync")
	}

	src := &staticSource{rows: nil}
	syncer := proxy.NewPoolSyncer(prx, src, noopTransport{}, slog.Default(), true)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	syncer.RunOnce(ctx)

	if !prx.SyncedOnce() {
		t.Fatal("SyncedOnce must be true after first sync")
	}
}

// TestPoolSyncer_SingleNodeUnchanged asserts that the onMissSync hook is nil
// and has no effect on single-node deployments. This is a behavioral test:
// when SetOnMissSync is never called, a miss still serves the loading page.
func TestPoolSyncer_SingleNodeUnchanged(t *testing.T) {
	prx := proxy.New()
	// Do NOT call SetOnMissSync (simulates single-node).

	req := httptest.NewRequest("GET", "/app/some-app/", nil)
	rec := httptest.NewRecorder()
	prx.ServeHTTP(rec, req)

	// Must serve loading page, not crash.
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (loading page), got %d", rec.Code)
	}
	body := rec.Body.String()
	if body == "on-miss" {
		t.Error("single-node must not trigger on-miss sync")
	}
	// Loading page contains the spinner.
	if len(body) < 100 {
		t.Errorf("expected loading page HTML, got short body: %q", body)
	}
}

// TestPoolSyncer_DeregistersGoneSlug guards the Deregister(slug) branch in
// sync(): when a slug's replicas vanish entirely from the routable source the
// whole pool entry is removed, not just individual slots within it.
func TestPoolSyncer_DeregistersGoneSlug(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer backend.Close()

	rr := makeReplica("vanish-app", 1, 0, backend.URL, 1)
	var src swappableSource
	src.set([]db.RoutableReplica{rr})

	prx := proxy.New()
	syncer := proxy.NewPoolSyncer(prx, &src, noopTransport{}, slog.Default(), true)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// First sync: pool is registered.
	syncer.RunOnce(ctx)
	if !prx.HasLiveReplica("vanish-app") {
		t.Fatal("pool must exist after first sync")
	}

	// Remove the slug entirely from the source (all replicas stopped/lost).
	src.set(nil)
	syncer.RunOnce(ctx)

	// The entire pool entry must be gone, not just the slot.
	if prx.HasLiveReplica("vanish-app") {
		t.Fatal("pool must be deregistered when slug disappears from routable source")
	}
	// A request for the slug now gets the loading page (no pool).
	req := httptest.NewRequest("GET", "/app/vanish-app/", nil)
	rec := httptest.NewRecorder()
	prx.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (loading page) after pool removed, got %d", rec.Code)
	}
	if rec.Body.String() == "ok" {
		t.Error("request must not reach the backend after deregistration")
	}
}

// TestPoolSyncer_StartupAdoption asserts that a single RunOnce pass on a fresh
// proxy with no existing pool registers routable replicas from the DB and calls
// MarkSynced. This is the startup pool adoption path: a successor process after
// a zero-downtime handoff calls RunOnce once before the watcher takes ownership,
// so the data plane is populated immediately rather than waiting ~10s for the
// ownership lease.
func TestPoolSyncer_StartupAdoption(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("adopted"))
	}))
	defer backend.Close()

	rr := makeReplica("adopted-app", 1, 0, backend.URL, 77)
	src := &staticSource{rows: []db.RoutableReplica{rr}}

	prx := proxy.New()
	// Do NOT call prx.MarkSynced() - we are testing that RunOnce does it.
	if prx.SyncedOnce() {
		t.Fatal("SyncedOnce must be false before RunOnce")
	}

	syncer := proxy.NewPoolSyncer(prx, src, noopTransport{}, slog.Default(), false)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	syncer.RunOnce(ctx)

	// MarkSynced must have been called by RunOnce.
	if !prx.SyncedOnce() {
		t.Error("RunOnce must call MarkSynced so /readyz is not blocked after startup adoption")
	}

	// The replica must be registered and routable immediately.
	req := httptest.NewRequest("GET", "/app/adopted-app/", nil)
	rec := httptest.NewRecorder()
	prx.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 after startup adoption, got %d (body: %s)", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "adopted" {
		t.Errorf("body = %q, want adopted", rec.Body.String())
	}
}

// TestPoolSyncer_RaceClean exercises the syncer goroutine under -race.
func TestPoolSyncer_RaceClean(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer backend.Close()

	rr := makeReplica("race-app", 1, 0, backend.URL, 1)
	src := &staticSource{rows: []db.RoutableReplica{rr}}

	prx := proxy.New()
	syncer := proxy.NewPoolSyncer(prx, src, noopTransport{}, slog.Default(), true)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	// Run the syncer loop in a goroutine while also doing proxy reads.
	go syncer.Run(ctx)

	for i := 0; i < 20; i++ {
		req := httptest.NewRequest("GET", "/app/race-app/", nil)
		rec := httptest.NewRecorder()
		prx.ServeHTTP(rec, req)
		time.Sleep(5 * time.Millisecond)
	}
	<-ctx.Done()
}

// makeReplicaWithIdentity returns a RoutableReplica with the given
// AppIdentityHeaders column value (nil = inherit global).
func makeReplicaWithIdentity(slug string, appID int64, identityCol *bool) db.RoutableReplica {
	depID := int64(1)
	return db.RoutableReplica{
		Slug:                  slug,
		AppMaxSessionsPerRepl: 0,
		AppIdentityHeaders:    identityCol,
		Replica: &db.Replica{
			AppID:        appID,
			Index:        0,
			Status:       db.ReplicaStatusRunning,
			EndpointURL:  "http://127.0.0.1:19999",
			DeploymentID: &depID,
			DesiredState: "running",
			Provider:     "fargate",
		},
	}
}

// TestReconcileSlug_PushesEffectiveIdentityFlag asserts that reconcileSlug
// resolves the tri-state column against the syncer's global flag and stores
// the result on the pool's identityHeaders atomic.
func TestReconcileSlug_PushesEffectiveIdentityFlag(t *testing.T) {
	f := false
	tr := true
	cases := []struct {
		name           string
		col            *bool
		identityGlobal bool
		want           bool
	}{
		{"column false overrides global on", &f, true, false},
		{"column NULL inherits global on", nil, true, true},
		{"column NULL inherits global off", nil, false, false},
		{"column true with global on", &tr, true, true},
		{"column true with global off", &tr, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := proxy.New()
			src := &staticSource{rows: []db.RoutableReplica{makeReplicaWithIdentity("demo", 1, c.col)}}
			syncer := proxy.NewPoolSyncer(p, src, noopTransport{}, slog.Default(), c.identityGlobal)

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			syncer.RunOnce(ctx)

			if got := p.PoolIdentityHeaders("demo"); got != c.want {
				t.Errorf("flag = %v, want %v", got, c.want)
			}
		})
	}
}

// TestPoolSyncer_RunOnce_NilDialerRemoteDocker is the regression for the
// control-plane startup panic: single-node startup pool adoption built the
// transport builder with a nil dialer, and reconciling a routable remote_docker
// replica dereferenced that nil dialer (SIGSEGV, "control plane did not start").
// With the nil-guard in TransportForReplica, the syncer logs the transport error
// and skips the slot, so the control plane survives and the replica is simply
// left unrouted. This drives the exact production path through the real
// worker.ReplicaTransportBuilder: RunOnce -> sync -> reconcileSlug ->
// TransportForReplica.
func TestPoolSyncer_RunOnce_NilDialerRemoteDocker(t *testing.T) {
	store := dbtest.New(t)

	// Seed owner -> app -> worker -> a routable remote_docker replica. The worker
	// MUST exist so GetWorker succeeds and (pre-fix) the nil dialer is actually
	// dereferenced; a missing worker returns ErrNotFound before the deref and would
	// not reproduce the panic.
	if err := store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: "h", Role: "admin"}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	owner, err := store.GetUserByUsername("owner")
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	const slug = "remote-app"
	if err := store.CreateApp(db.CreateAppParams{Slug: slug, Name: "remote", OwnerID: owner.ID, Access: "private"}); err != nil {
		t.Fatalf("create app: %v", err)
	}
	app, err := store.GetAppBySlug(slug)
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	if err := store.UpsertWorker(db.Worker{NodeID: "node-a", Tier: "remote", AdvertiseAddr: "192.0.2.5:8443", Status: "up"}); err != nil {
		t.Fatalf("upsert worker: %v", err)
	}
	depID := int64(1)
	if err := store.UpsertReplica(db.UpsertReplicaParams{
		AppID:        app.ID,
		Index:        0,
		Status:       db.ReplicaStatusRunning,
		Provider:     "remote_docker",
		Tier:         "remote",
		WorkerID:     "node-a",
		EndpointURL:  "http://192.0.2.5:8080",
		DesiredState: "running",
		DeploymentID: &depID,
	}); err != nil {
		t.Fatalf("upsert replica: %v", err)
	}

	// Build the syncer exactly as single-node startup adoption did: the transport
	// builder is constructed with a NIL dialer.
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	prx := proxy.New()
	prx.MarkSynced()
	syncer := proxy.NewPoolSyncer(prx, store, worker.NewReplicaTransportBuilder(nil, store), logger, false)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Must not panic (pre-fix this is a SIGSEGV inside TransportForReplica).
	syncer.RunOnce(ctx)

	// The transport error is logged and the slot skipped. Assert the nil-dialer
	// cause specifically (not just the generic event) so a future seed change that
	// makes GetWorker fail instead can't silently turn this into a false pass.
	if !strings.Contains(logBuf.String(), "pool_sync_transport_error") {
		t.Errorf("expected pool_sync_transport_error to be logged; log was:\n%s", logBuf.String())
	}
	if !strings.Contains(logBuf.String(), "no agent dialer configured") {
		t.Errorf("expected the nil-dialer error cause to be logged; log was:\n%s", logBuf.String())
	}
	// ...and no backend target is registered for the slug. SetPoolSize runs before
	// transport construction, so assert the target URL (not RegisteredSlugs).
	if got := prx.ReplicaTargetURL(slug, 0); got != "" {
		t.Errorf("ReplicaTargetURL(%q, 0) = %q, want \"\" (no route registered)", slug, got)
	}
}
