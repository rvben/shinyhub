package lifecycle

import (
	"syscall"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
)

func TestLiveLifetimeIncreaseAndDisablePreserveWorker(t *testing.T) {
	for _, limit := range []int{0, 3, -1} {
		name := "disabled"
		if limit > 0 {
			name = "extended"
		} else if limit < 0 {
			name = "removed"
		}
		t.Run(name, func(t *testing.T) {
			store := dbtest.New(t)
			if err := store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: "h", Role: "admin"}); err != nil {
				t.Fatal(err)
			}
			owner, err := store.GetUserByUsername("owner")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.CreateApp(db.CreateAppParams{Slug: "app", Name: "app", OwnerID: owner.ID}); err != nil {
				t.Fatal(err)
			}
			if _, err := store.PatchAppSettings(db.PatchAppSettingsParams{
				Slug: "app", SetWorkerMaxSessionLifetime: true, WorkerMaxSessionLifetimeSecs: 1,
			}); err != nil {
				t.Fatal(err)
			}
			p := proxy.New()
			p.SetPoolMode("app", config.IsolationGrouped, 2, 2)
			if err := p.RegisterElasticWorker("app", 0, "http://127.0.0.1:1", nil, 0); err != nil {
				t.Fatal(err)
			}
			mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
			old, err := mgr.Start(process.StartParams{Slug: "app", Index: 0, Dir: t.TempDir(), Command: []string{"sleep", "30"}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = mgr.Stop("app") })
			s := &ElasticSpawner{Store: store, Manager: mgr, Proxy: p}
			t.Cleanup(func() { s.CancelLifetime("app", 0) })
			s.WarmSpareConsumed("app", 0, p.PoolEpoch("app"))
			if limit < 0 {
				p.SetCancelElasticLifetimeFunc(s.CancelLifetime)
				p.DeregisterElasticWorker("app", 0)
			} else {
				s.UpdateSessionLifetime("app", limit)
			}
			time.Sleep(1250 * time.Millisecond)
			if err := syscall.Kill(old.PID, 0); err != nil {
				t.Fatalf("previous deadline killed the live worker: %v", err)
			}
			if limit > 0 {
				deadline := time.Now().Add(3 * time.Second)
				for syscall.Kill(old.PID, 0) == nil {
					if time.Now().After(deadline) {
						t.Fatal("extended lifetime was never enforced")
					}
					time.Sleep(10 * time.Millisecond)
				}
			}
		})
	}
}
