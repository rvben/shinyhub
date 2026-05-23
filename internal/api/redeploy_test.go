package api

import (
	"encoding/json"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
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

func TestDeployLock_TryAcquireFailsWhenHeld(t *testing.T) {
	s := &Server{cfg: &config.Config{}}
	const slug = "busy"

	release := s.acquireDeployLock(slug)
	defer release()

	if got := s.tryAcquireDeployLock(slug); got != nil {
		t.Fatal("tryAcquireDeployLock should return nil while the lock is held")
	}
}

func TestDeployLock_TryAcquireSucceedsAfterRelease(t *testing.T) {
	s := &Server{cfg: &config.Config{}}
	const slug = "now-free"

	release := s.acquireDeployLock(slug)
	release()

	r2 := s.tryAcquireDeployLock(slug)
	if r2 == nil {
		t.Fatal("tryAcquireDeployLock should succeed after the previous holder released")
	}
	r2()
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
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	hash, _ := auth.HashPassword("pass")
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

// TestRedeployApp_CoalescesConcurrent keeps the historical guarantee that the
// async redeployApp goroutine skips when another deploy is already in flight,
// so a flurry of /apps/:slug PATCHes doesn't pile up redeploys.
func TestRedeployApp_CoalescesConcurrent(t *testing.T) {
	s := &Server{cfg: &config.Config{}}
	const slug = "myapp"

	first := s.acquireDeployLock(slug)
	defer first()

	if got := s.tryAcquireDeployLock(slug); got != nil {
		t.Fatal("redeploy coalesce: tryAcquireDeployLock must return nil while another is in flight")
	}
}
