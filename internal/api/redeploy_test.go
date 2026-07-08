package api

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
)

// TestDeployLock_SerializesSameSlug proves that two goroutines acquiring the
// per-slug deploy lock for the SAME slug are forced to run sequentially,
// while two different slugs run independently. This is the invariant that
// guards every deploy/restart/rollback/stop/delete code path against the
// state-corruption you get when two of them mutate the same app at once.
func TestDeployLock_SerializesSameSlug(t *testing.T) {
	s := &Server{cfg: &config.Config{}}

	var inFlight, maxObserved int32
	work := func() {
		now := atomic.AddInt32(&inFlight, 1)
		for {
			cur := atomic.LoadInt32(&maxObserved)
			if cur >= now || atomic.CompareAndSwapInt32(&maxObserved, cur, now) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		atomic.AddInt32(&inFlight, -1)
	}

	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release := s.acquireDeployLock("same-slug")
			defer release()
			work()
		}()
	}
	wg.Wait()
	if got := atomic.LoadInt32(&maxObserved); got != 1 {
		t.Fatalf("same-slug acquisitions overlapped: peak in-flight=%d, want 1", got)
	}
}

func TestDeployLock_DifferentSlugsIndependent(t *testing.T) {
	s := &Server{cfg: &config.Config{}}

	const slugs = 4
	start := make(chan struct{})
	var ready sync.WaitGroup
	ready.Add(slugs)
	for i := range slugs {
		go func() {
			release := s.acquireDeployLock("slug-" + string(rune('a'+i)))
			defer release()
			ready.Done()
			<-start
		}()
	}
	ready.Wait()
	// All 4 different-slug locks were acquired without serialization. Release.
	close(start)
}

// TestRedeployInFlight_Lifecycle covers the marker the PATCH handler sets
// synchronously before launching the async redeploy, so a --wait client never
// mistakes the still-"running" app row for a completed pool restart.
func TestRedeployInFlight_Lifecycle(t *testing.T) {
	s := &Server{cfg: &config.Config{}}
	if s.isRedeployInFlight("demo") {
		t.Fatal("a fresh slug must not be reported in flight")
	}
	s.markRedeployInFlight("demo")
	if !s.isRedeployInFlight("demo") {
		t.Fatal("after mark, the slug must be reported in flight")
	}
	if s.isRedeployInFlight("other") {
		t.Fatal("marking one slug must not report another in flight")
	}
	s.clearRedeployInFlight("demo")
	if s.isRedeployInFlight("demo") {
		t.Fatal("after clear, the slug must not be reported in flight")
	}
}

// TestHandleGetApp_AdvertisesRedeployInFlight proves the GET response carries
// the marker so a polling client can distinguish "pool already cycled" from
// "redeploy still in flight" even while app.status stays "running".
func TestHandleGetApp_AdvertisesRedeployInFlight(t *testing.T) {
	store := dbtest.New(t)
	hash, _ := testHashPassword("pass")
	if err := store.CreateUser(db.CreateUserParams{Username: "bob", PasswordHash: hash, Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateApp(db.CreateAppParams{Slug: "demo", Name: "Demo", OwnerID: 1}); err != nil {
		t.Fatal(err)
	}

	srv := New(&config.Config{Auth: config.AuthConfig{Secret: "test-secret"}}, store, nil, nil)
	srv.markRedeployInFlight("demo")

	token, _ := auth.IssueJWT(1, "bob", "admin", "test-secret")
	req := httptest.NewRequest("GET", "/api/apps/demo", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		RedeployInFlight bool `json:"redeploy_in_flight"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.RedeployInFlight {
		t.Fatalf("GET app must advertise redeploy_in_flight while marked; body=%s", rec.Body.String())
	}
}

// TestPatchApp_ResourceLimitChangeTriggersRedeploy proves a memory/cpu limit
// change on a running app cycles the pool (so the new cgroup ceiling reaches the
// running replicas), the same way a replica-count change does. The pre-held
// deploy lock blocks the launched redeployApp goroutine so the synchronously-set
// marker can be observed deterministically.
func TestPatchApp_ResourceLimitChangeTriggersRedeploy(t *testing.T) {
	const slug = "limited"
	store, _ := newRedeployTestStore(t, slug, "running")
	s := New(&config.Config{Auth: config.AuthConfig{Secret: "test-secret"}}, store,
		process.NewManager(t.TempDir(), process.NewNativeRuntime()), proxy.New())

	release := s.acquireDeployLock(slug)
	defer release()

	patch := map[string]any{"cpu_quota_percent": 150}
	body, _ := json.Marshal(patch)
	token, _ := auth.IssueJWT(1, "bob", "admin", "test-secret")
	req := httptest.NewRequest("PATCH", "/api/apps/"+slug, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.Router().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("PATCH cpu_quota_percent: got %d, want 200: %s", rec.Code, rec.Body.String())
	}

	if !s.isRedeployInFlight(slug) {
		t.Fatal("a resource-limit change on a running app must mark redeploy_in_flight (cycle the pool)")
	}
}

// newRedeployTestStore returns an in-memory store seeded with one admin user
// and an app at the given status, plus the app row, for redeployApp tests.
func newRedeployTestStore(t *testing.T, slug, status string) (*db.Store, *db.App) {
	t.Helper()
	store := dbtest.New(t)
	hash, _ := testHashPassword("pass")
	if err := store.CreateUser(db.CreateUserParams{Username: "bob", PasswordHash: hash, Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateApp(db.CreateAppParams{Slug: slug, Name: slug, OwnerID: 1}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: slug, Status: status}); err != nil {
		t.Fatal(err)
	}
	app, err := store.GetAppBySlug(slug)
	if err != nil {
		t.Fatal(err)
	}
	return store, app
}

// TestRedeployApp_BlocksUntilLockFreeThenClears proves the async redeploy waits
// for the per-slug deploy lock rather than skipping when an UNRELATED operation
// (upload deploy, restart, rollback, stop, delete) holds it. Skipping would
// drop the replica change entirely and either wedge the in-flight marker (if it
// never cleared) or falsely report the pool ready (if it cleared). The marker
// must stay set while the holder runs, and only clear once the redeploy
// actually gets its turn.
func TestRedeployApp_BlocksUntilLockFreeThenClears(t *testing.T) {
	const slug = "busy-app"
	// status "stopped" makes redeployApp short-circuit after it finally
	// acquires the lock, so the test exercises the lock-wait + marker balance
	// without needing a live process manager.
	store, _ := newRedeployTestStore(t, slug, "stopped")
	s := New(&config.Config{}, store, nil, nil)

	// An unrelated operation holds the deploy lock for this slug.
	release := s.acquireDeployLock(slug)

	s.markRedeployInFlight(slug)
	done := make(chan struct{})
	go func() {
		s.redeployApp(slug)
		close(done)
	}()

	// While the lock is held, redeployApp must block: the marker stays set and
	// the goroutine has not returned.
	time.Sleep(50 * time.Millisecond)
	if !s.isRedeployInFlight(slug) {
		release()
		t.Fatal("redeployApp cleared the marker while the deploy lock was held; " +
			"it must block until the holder releases, not skip-and-clear")
	}
	select {
	case <-done:
		release()
		t.Fatal("redeployApp returned while the deploy lock was held; it must wait for the holder")
	default:
	}

	// Once the holder releases, redeployApp proceeds and clears the marker.
	release()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("redeployApp did not complete after the deploy lock was released")
	}
	if s.isRedeployInFlight(slug) {
		t.Fatal("redeployApp completed but left redeploy_in_flight set")
	}
}

// TestRedeployApp_DoesNotResurrectStoppedApp proves that when a concurrent stop
// (or hibernate/delete) changes the app away from running while the redeploy is
// waiting for the deploy lock, the redeploy honours that terminal intent
// instead of cycling the pool back up. Without the guard the blocking redeploy
// would always win the race and resurrect an app the operator just tore down.
func TestRedeployApp_DoesNotResurrectStoppedApp(t *testing.T) {
	const slug = "torn-down"
	store, app := newRedeployTestStore(t, slug, "stopped")
	// A live (promoted) deployment exists, so without the status guard
	// redeployApp would reach deploy.Run and (failing to boot the empty bundle)
	// drive the app to "degraded" - an observable resurrection attempt.
	// ListDeployments excludes pending rows, so the deployment must be promoted.
	dep, err := store.BeginDeployment(app.ID, "v1", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PromoteDeployment(dep.ID); err != nil {
		t.Fatal(err)
	}
	s := New(&config.Config{}, store, process.NewManager(t.TempDir(), process.NewNativeRuntime()), proxy.New())

	s.markRedeployInFlight(slug)
	s.redeployApp(slug)

	got, err := store.GetAppBySlug(slug)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "stopped" {
		t.Fatalf("redeployApp altered a stopped app to %q; a concurrent stop must win over a queued replica redeploy", got.Status)
	}
	if s.isRedeployInFlight(slug) {
		t.Fatal("redeployApp must clear the marker even when it skips a torn-down app")
	}
}

// TestRedeployInFlight_RefcountedAcrossCoalescedMarks proves the in-flight
// marker is reference counted: a coalesced second PATCH marks again, and its
// goroutine then clears one reference, but the marker must stay set while the
// FIRST redeploy is still cycling the pool. Only the last clear (the active
// pool-cycler completing) reports the pool idle. Without refcounting the
// coalesced clear would prematurely advertise the pool ready and a --wait
// client would return against the old pool.
func TestRedeployInFlight_RefcountedAcrossCoalescedMarks(t *testing.T) {
	s := &Server{cfg: &config.Config{}}
	const slug = "demo"
	s.markRedeployInFlight(slug)  // first PATCH
	s.markRedeployInFlight(slug)  // second (coalesced) PATCH
	s.clearRedeployInFlight(slug) // second goroutine completes and clears one ref
	if !s.isRedeployInFlight(slug) {
		t.Fatal("first redeploy still cycling the pool; marker must remain set " +
			"until its goroutine clears the last reference")
	}
	s.clearRedeployInFlight(slug) // first redeploy completes
	if s.isRedeployInFlight(slug) {
		t.Fatal("after the last clear the pool is idle and must report not in flight")
	}
}
