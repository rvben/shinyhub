package lifecycle

import (
	"sync"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
)

func TestWarmSpareConsumedIgnoresReplacedPool(t *testing.T) {
	store := dbtest.New(t)
	if err := store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: "h", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	owner, err := store.GetUserByUsername("owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "app", Name: "app", OwnerID: owner.ID, Access: "private"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec("UPDATE apps SET worker_max_session_lifetime_secs=3600 WHERE slug='app'"); err != nil {
		t.Fatal(err)
	}
	p := proxy.New()
	p.SetPoolMode("app", config.IsolationGrouped, 4, 4)
	oldEpoch := p.PoolEpoch("app")
	p.Deregister("app")
	p.SetPoolMode("app", config.IsolationGrouped, 4, 4)
	if err := p.RegisterElasticWorker("app", 0, "http://replacement/", nil, 30); err != nil {
		t.Fatal(err)
	}
	s := &ElasticSpawner{Proxy: p, Store: store}
	defer s.CancelLifetime("app", 0)
	s.WarmSpareConsumed("app", 0, oldEpoch)
	if _, armed := s.lifetimeTimers.Load("app/0"); armed {
		t.Fatal("old assignment armed a lifetime timer for the replacement")
	}
	s.WarmSpareConsumed("app", 0, p.PoolEpoch("app"))
	if _, armed := s.lifetimeTimers.Load("app/0"); !armed {
		t.Fatal("current assignment did not arm its lifetime timer")
	}
}

func TestGroupedRetirementCancelsLifetimeBeforeSlotReuse(t *testing.T) {
	for _, cancel := range []bool{true, false} {
		name := "epoch_fences_queued_callback"
		if cancel {
			name = "retirement_cancels_queued_callback"
		}
		t.Run(name, func(t *testing.T) {
			p := proxy.New()
			p.SetPoolMode("app", config.IsolationGrouped, 4, 4)
			if err := p.RegisterElasticWorker("app", 0, "http://old/", nil, 10); err != nil {
				t.Fatal(err)
			}
			s := &ElasticSpawner{Proxy: p, Manager: process.NewManager(t.TempDir(), process.NewNativeRuntime())}
			if cancel {
				p.SetCancelElasticLifetimeFunc(s.CancelLifetime)
			}
			s.armLifetime(&db.App{WorkerMaxSessionLifetimeSecs: 3600}, "app", 0)
			defer s.CancelLifetime("app", 0)
			v, ok := s.lifetimeTimers.Load("app/0")
			if !ok {
				t.Fatal("old worker has no lifetime timer")
			}
			old := v.(*elasticLifetime)
			// Trigger expiration deterministically, already queued behind the
			// deployment fence when retirement and replacement happen.
			old.timer.Stop()
			entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
			unblock := sync.OnceFunc(func() { close(release) })
			defer unblock()
			s.AcquireAppOperation = func(string) (func(), error) {
				close(entered)
				<-release
				return func() {}, nil
			}
			go func() {
				defer close(done)
				s.expireLifetime("app", 0, old)
			}()
			<-entered
			slot, err := p.StageGroupedGeneration("app", 20)
			if err != nil {
				t.Fatal(err)
			}
			if err := p.RegisterGenerationReplica("app", 20, slot, "http://new/", nil); err != nil {
				t.Fatal(err)
			}
			if _, err := p.ActivateGeneration("app", 20); err != nil {
				t.Fatal(err)
			}
			if !p.RetireGeneration("app", 10) {
				t.Fatal("old generation was not retired")
			}
			if _, armed := s.lifetimeTimers.Load("app/0"); cancel && armed {
				t.Fatal("retirement retained old lifetime timer")
			}
			p.Deregister("app")
			p.SetPoolMode("app", config.IsolationGrouped, 4, 4)
			if err := p.RegisterElasticWorker("app", 0, "http://replacement/", nil, 30); err != nil {
				t.Fatal(err)
			}
			// The replacement has no timer: disabling lifetime or leaving a
			// pristine spare must not expose it to an earlier incarnation.
			unblock()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("queued expiration did not finish")
			}
			if p.ElasticWorkerCount("app") != 1 {
				t.Fatal("old timer removed the replacement worker")
			}
		})
	}
}

// TestElasticHibernate_OldLifetimeBackstopDoesNotTerminateNewWorker covers
// the hibernate/wake counterpart of TestGroupedRetirementCancelsLifetime
// BeforeSlotReuse's epoch-fence case: a lifetime timer armed before hibernate
// that is never explicitly cancelled (the watcher's real hibernate path does
// cancel it via TerminateConfirmed, but this proves the fence holds even when
// that does not happen, e.g. a stop that raced the timer) must not reach past
// BeginHibernate's epoch bump and terminate a worker that starts after wake.
// Before that bump, BeginHibernate left the pool epoch unchanged across
// hibernate/wake, so a stale backstop's epoch check passed and it tore down
// whatever now occupied its slot.
func TestElasticHibernate_OldLifetimeBackstopDoesNotTerminateNewWorker(t *testing.T) {
	p := proxy.New()
	p.SetPoolMode("app", config.IsolationGrouped, 4, 4)
	if err := p.RegisterElasticWorker("app", 0, "http://old/", nil, 10); err != nil {
		t.Fatal(err)
	}
	s := &ElasticSpawner{Proxy: p, Manager: process.NewManager(t.TempDir(), process.NewNativeRuntime())}
	s.armLifetime(&db.App{WorkerMaxSessionLifetimeSecs: 3600}, "app", 0)
	defer s.CancelLifetime("app", 0)
	v, ok := s.lifetimeTimers.Load("app/0")
	if !ok {
		t.Fatal("old worker has no lifetime timer")
	}
	old := v.(*elasticLifetime)
	// Trigger expiration deterministically instead of waiting out the real
	// AfterFunc: stop the timer and invoke its callback body directly.
	old.timer.Stop()

	if !p.BeginHibernate("app", time.Now()) {
		t.Fatal("BeginHibernate refused to hibernate a genuinely idle elastic pool")
	}
	// Wake and spawn: the watcher rebuilds the pool via SetPoolMode and a
	// fresh worker registers into it, the way a demand spawn does.
	p.SetPoolMode("app", config.IsolationGrouped, 4, 4)
	if err := p.RegisterElasticWorker("app", 0, "http://new/", nil, 20); err != nil {
		t.Fatal(err)
	}

	// The pre-hibernate backstop fires late, landing after wake and the new
	// worker's registration.
	s.expireLifetime("app", 0, old)

	if p.ElasticWorkerCount("app") != 1 {
		t.Fatal("stale lifetime backstop from the pre-hibernate generation terminated the post-wake worker")
	}
}
