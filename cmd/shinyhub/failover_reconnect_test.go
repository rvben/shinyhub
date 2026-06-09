package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
	"github.com/rvben/shinyhub/internal/leader"
	"github.com/rvben/shinyhub/internal/proxy"
)

// noopTransportBuilder returns nil for every replica, directing the proxy to
// use http.DefaultTransport. Correct for fargate replicas (plain HTTP to a
// VPC private IP); nil also works here because the test backend is a plain
// httptest.Server reachable via the default transport.
type noopTransportBuilder struct{}

func (noopTransportBuilder) TransportForReplica(_ *db.Replica) (http.RoundTripper, error) {
	return nil, nil
}

// TestFailoverReconnect_StandbyServesSameReplica is the capstone integration
// test for HA Phase 4 data-plane independence.
//
// It proves three guarantees that together constitute "seamless reconnect":
//
//  1. Both proxy instances (A and B) serve the same off-host fargate replica
//     independently of the control-plane lease, after one RunOnce sync.
//  2. The sticky cookie issued by A routes B to the SAME replica index
//     (cross-instance affinity), proven by presenting A's Set-Cookie to B.
//  3. After A is crashed, B's data plane serves the still-running backend
//     IMMEDIATELY - without waiting for the lease TTL to expire and without
//     any app restart. The backend request counter keeps climbing, never
//     resets to zero, proving the backend was never stopped or restarted.
//
// Only after these three data-plane assertions does the test wait for the
// control-plane lease handover (gate B opens), confirming the full failover
// path also completes.
//
// Runs on SQLite and, when SHINYHUB_TEST_POSTGRES_DSN is set, Postgres.
// Safe under -race.
func TestFailoverReconnect_StandbyServesSameReplica(t *testing.T) {
	store := dbtest.New(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// TTL=1s so the lease expires quickly during the control-plane failover
	// assertion at the end. RenewEvery=100ms keeps A's lease alive while it
	// is the owner.
	const (
		ttl        = 1 * time.Second
		renewEvery = 100 * time.Millisecond
	)

	// --- backend: a real httptest.Server standing in for a fargate replica ---
	// Each request increments a counter so the test can prove the backend was
	// never restarted (a restart would reset the counter to zero).
	var backendHits atomic.Int64
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backendHits.Add(1)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("hit")) //nolint:errcheck
	}))
	t.Cleanup(backend.Close)

	// --- DB: create a public app, a deployment, and one running fargate replica ---
	if err := store.CreateUser(db.CreateUserParams{
		Username:     "owner",
		PasswordHash: "h",
		Role:         "admin",
	}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	owner, err := store.GetUserByUsername("owner")
	if err != nil {
		t.Fatalf("get user: %v", err)
	}

	const slug = "demo-app"
	if err := store.CreateApp(db.CreateAppParams{
		Slug:    slug,
		Name:    slug,
		OwnerID: owner.ID,
		Access:  "public",
	}); err != nil {
		t.Fatalf("create app: %v", err)
	}
	app, err := store.GetAppBySlug(slug)
	if err != nil {
		t.Fatalf("get app: %v", err)
	}

	dep, err := store.CreateDeployment(db.CreateDeploymentParams{
		AppID:     app.ID,
		Version:   "v1",
		BundleDir: "/tmp/test-bundle",
	})
	if err != nil {
		t.Fatalf("create deployment: %v", err)
	}

	if err := store.UpsertReplica(db.UpsertReplicaParams{
		AppID:        app.ID,
		Index:        0,
		Status:       db.ReplicaStatusRunning,
		Provider:     "fargate",
		Tier:         "fargate",
		EndpointURL:  backend.URL,
		DesiredState: "running",
		DeploymentID: &dep.ID,
	}); err != nil {
		t.Fatalf("upsert replica: %v", err)
	}

	// --- shared sticky secret: same key on both instances gives cross-instance affinity ---
	stickyKey := []byte("test-sticky-secret-shared-key-32b")

	// --- proxy A and proxy B, each with their own PoolSyncer over the shared store ---
	prxA := proxy.New()
	prxA.SetStickySecret(stickyKey)
	syncerA := proxy.NewPoolSyncer(prxA, store, noopTransportBuilder{}, logger)

	prxB := proxy.New()
	prxB.SetStickySecret(stickyKey)
	syncerB := proxy.NewPoolSyncer(prxB, store, noopTransportBuilder{}, logger)

	ctx := context.Background()

	// Sync both pools once - deterministic, no ticker racing.
	syncerA.RunOnce(ctx)
	syncerB.RunOnce(ctx)

	// --- precondition: both proxies route to the backend after sync ---
	reqA := httptest.NewRequest(http.MethodGet, "/app/"+slug+"/", nil)
	recA := httptest.NewRecorder()
	prxA.ServeHTTP(recA, reqA)
	if recA.Code != http.StatusOK {
		t.Fatalf("proxy A pre-crash: expected 200, got %d", recA.Code)
	}
	if body := recA.Body.String(); body != "hit" {
		t.Fatalf("proxy A pre-crash: body = %q, want hit", body)
	}

	// Extract the sticky cookie A set. A non-empty Set-Cookie proves A picked
	// a replica and stamped the deployment ID into the cookie.
	cookieName := "shinyhub_rep_" + slug
	var stickyCookieValue string
	for _, c := range recA.Result().Cookies() {
		if c.Name == cookieName {
			stickyCookieValue = c.Value
			break
		}
	}
	if stickyCookieValue == "" {
		t.Fatal("proxy A did not set a sticky cookie - cannot assert cross-instance affinity")
	}

	// --- cross-instance affinity: present A's cookie to B, assert B routes to same backend ---
	// A sticky hit does NOT re-issue a Set-Cookie (ServeHTTP only sets the cookie
	// on a re-pick). The absence of a Set-Cookie header proves B verified A's
	// HMAC-signed cookie (same shared key + deployment_id) and honored the sticky
	// pin, rather than coincidentally re-picking the same index.
	reqB1 := httptest.NewRequest(http.MethodGet, "/app/"+slug+"/", nil)
	reqB1.AddCookie(&http.Cookie{Name: cookieName, Value: stickyCookieValue})
	recB1 := httptest.NewRecorder()
	prxB.ServeHTTP(recB1, reqB1)
	if recB1.Code != http.StatusOK {
		t.Fatalf("proxy B (affinity): expected 200, got %d (body: %s)", recB1.Code, recB1.Body.String())
	}
	if body := recB1.Body.String(); body != "hit" {
		t.Fatalf("proxy B (affinity): body = %q, want hit (backend not reached)", body)
	}
	// No re-issue means B verified A's HMAC and honored the sticky pin.
	if cookies := recB1.Result().Cookies(); len(cookies) != 0 {
		t.Fatalf("proxy B (affinity): got %d Set-Cookie header(s), want none - B re-picked instead of honoring A's HMAC-signed cookie", len(cookies))
	}
	// The backend counter must be >= 2 at this point (A hit + B affinity hit).
	if n := backendHits.Load(); n < 2 {
		t.Fatalf("backend hit count after A+B affinity requests = %d, want >= 2", n)
	}

	// --- wire electors so A is the active owner and B is the standby ---
	var readyA, readyB atomic.Bool

	makeOwnerWork := func(ready *atomic.Bool) func(context.Context, int64) {
		return func(octx context.Context, _ int64) {
			ready.Store(false)
			defer ready.Store(false)
			// This harness exercises the proxy pool and the gate predicate.
			// The worker registry refresh (refreshUntilReady) is omitted here
			// because this test focuses on data-plane independence from the
			// control-plane lease, not worker registry consistency.
			ready.Store(true)
			<-octx.Done()
		}
	}

	scopeA := leader.NewOwnerScope(makeOwnerWork(&readyA))
	scopeB := leader.NewOwnerScope(makeOwnerWork(&readyB))
	defer scopeA.Stop()
	defer scopeB.Stop()

	crashA := &crashableOwnerStore{Store: store}
	electorA := leader.New(crashA, leader.Config{
		InstanceID: "a", TTL: ttl, RenewEvery: renewEvery,
		OnAcquire: scopeA.Acquire, OnLose: scopeA.Lose, Logger: logger,
	})
	electorB := leader.New(store, leader.Config{
		InstanceID: "b", TTL: ttl, RenewEvery: renewEvery,
		OnAcquire: scopeB.Acquire, OnLose: scopeB.Lose, Logger: logger,
	})

	gateA := ownerAndReadyPredicate(electorA.IsOwner, &readyA)
	gateB := ownerAndReadyPredicate(electorB.IsOwner, &readyB)

	// Start A first and let it settle as the owner so B is deterministically
	// the standby.
	ctxA, cancelA := context.WithCancel(context.Background())
	defer cancelA()
	go electorA.Run(ctxA)
	failoverWaitFor(t, 10*time.Second, gateA, "A never became owner-and-ready")

	ctxB, cancelB := context.WithCancel(context.Background())
	defer cancelB()
	go electorB.Run(ctxB)

	if gateB() {
		t.Fatal("standby B opened its gate while A held the lease")
	}

	// Record the backend hit count just before the crash so we can verify it
	// only ever grows (never resets) after the crash.
	hitsBeforeCrash := backendHits.Load()

	// --- THE CRASH: A's renewals fail; it never releases the lease ---
	crashA.crashed.Store(true)

	// --- CORE ASSERTION (data plane, pre-lease-handover):
	//
	// Immediately after the crash - WITHOUT waiting for the TTL to expire and
	// WITHOUT waiting for B to acquire the lease - a fresh request to B's proxy
	// must reach the SAME running backend (HTTP 200, body "hit"). This is the
	// browser's reconnect: it targets whatever instance is available (here B)
	// and relies on the sticky cookie to reach the same running app replica.
	//
	// The backend was never stopped or restarted; its hit counter keeps climbing
	// past hitsBeforeCrash. The pool syncer on B populated the pool from the DB
	// before the crash happened; the data plane is fully independent of the
	// control-plane lease.
	reqBReconnect := httptest.NewRequest(http.MethodGet, "/app/"+slug+"/", nil)
	reqBReconnect.AddCookie(&http.Cookie{Name: cookieName, Value: stickyCookieValue})
	recBReconnect := httptest.NewRecorder()
	prxB.ServeHTTP(recBReconnect, reqBReconnect)

	if recBReconnect.Code != http.StatusOK {
		t.Fatalf("reconnect on B immediately after crash: expected 200, got %d (body: %s)",
			recBReconnect.Code, recBReconnect.Body.String())
	}
	if body := recBReconnect.Body.String(); body != "hit" {
		t.Fatalf("reconnect on B: body = %q, want hit (backend unreachable or restarted)", body)
	}
	// No re-issue: B honored the HMAC-signed cookie from A on the reconnect
	// request, proving cross-instance affinity holds post-crash.
	if cookies := recBReconnect.Result().Cookies(); len(cookies) != 0 {
		t.Fatalf("reconnect on B: got %d Set-Cookie header(s), want none - B re-picked instead of honoring A's cookie", len(cookies))
	}

	// The backend hit counter must be strictly higher than it was before the
	// crash: the reconnect reached the SAME backend, which was never restarted.
	if n := backendHits.Load(); n <= hitsBeforeCrash {
		t.Fatalf("backend hit count after reconnect = %d, want > %d (backend appears restarted or unreachable)",
			n, hitsBeforeCrash)
	}

	// B must NOT yet be the owner: the lease TTL has not expired. This confirms
	// the data-plane assertion above succeeded WITHOUT the lease handover.
	if gateB() {
		t.Fatal("B already became owner before TTL expired - data-plane assertion order violated")
	}

	// --- CONTROL-PLANE FAILOVER: B acquires the lease after the TTL expires ---
	// This is the same assertion as in TestFailover_StandbyTakesOverAndRoutes.
	failoverWaitFor(t, 10*time.Second, gateB,
		"standby B never became owner-and-ready after the active crashed")

	// The crashed instance must have relinquished its gate once the lease expired.
	failoverWaitFor(t, 5*time.Second, func() bool { return !gateA() },
		"crashed instance A never relinquished its gate after lease expiry")
}
