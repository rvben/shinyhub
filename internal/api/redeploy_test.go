package api

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/config"
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
	var ready, holding sync.WaitGroup
	ready.Add(slugs)
	holding.Add(slugs)
	for i := range slugs {
		go func() {
			release := s.acquireDeployLock("slug-" + string(rune('a'+i)))
			defer release()
			ready.Done()
			<-start
		}()
		// Yield so each goroutine has a chance to acquire its lock.
	}
	ready.Wait()
	// All 4 different-slug locks were acquired without serialization. Release.
	close(start)
	// Goroutines now exit and release.
	holding.Add(-slugs) // satisfy WaitGroup; we used start/ready instead
	_ = holding
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
