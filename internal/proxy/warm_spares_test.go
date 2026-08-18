package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/config"
)

func TestReconcileElasticWarmSpares_ProvisionsConfiguredFloor(t *testing.T) {
	p := New()
	p.SetPoolMode("warm", config.IsolationPerSession, 1, 4)
	p.SetPoolWarmSpares("warm", 2)
	spawned := make(chan int, 2)
	p.SetSpawnFunc(func(_ string, slotID int) { spawned <- slotID })

	p.ReconcileElasticWarmSpares("warm")
	gotSlots := map[int]bool{}
	for range 2 {
		select {
		case got := <-spawned:
			gotSlots[got] = true
		case <-time.After(time.Second):
			t.Fatal("warm-spare spawn callback not invoked")
		}
	}
	if !gotSlots[0] || !gotSlots[1] {
		t.Fatalf("spawned slots = %v, want 0 and 1", gotSlots)
	}

	snap, ok := p.ElasticWorkersSnapshot("warm")
	if !ok || snap.WarmSpareTarget != 2 || len(snap.Workers) != 2 {
		t.Fatalf("snapshot = %+v ok=%v, want two warm reservations", snap, ok)
	}
	for _, w := range snap.Workers {
		if !w.WarmSpare || w.Status != "booting" {
			t.Fatalf("worker = %+v, want booting warm spare", w)
		}
	}
}

func TestRunningElasticWarmSpare_NotifiesConsumptionAndReplenishes(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer backend.Close()

	p := New()
	p.SetPoolMode("warm", config.IsolationPerSession, 1, 3)
	p.SetPoolWarmSpares("warm", 1)
	spawned := make(chan int, 2)
	consumed := make(chan int, 1)
	p.SetSpawnFunc(func(_ string, slotID int) { spawned <- slotID })
	p.SetWarmSpareConsumedFunc(func(_ string, slotID int) { consumed <- slotID })
	p.ReconcileElasticWarmSpares("warm")
	slotID := <-spawned
	if err := p.RegisterElasticWorker("warm", slotID, backend.URL, nil, 7); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest("GET", "/app/warm/", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("first request status = %d, want backend response", rec.Code)
	}
	select {
	case got := <-consumed:
		if got != slotID {
			t.Fatalf("consumed slot = %d, want %d", got, slotID)
		}
	case <-time.After(time.Second):
		t.Fatal("running warm-spare consumption callback not invoked")
	}
	select {
	case replacement := <-spawned:
		if replacement == slotID {
			t.Fatalf("replacement reused consumed slot %d", slotID)
		}
	case <-time.After(time.Second):
		t.Fatal("running warm spare was not replenished")
	}
}

func TestSuspendedElasticWarmSpare_IsResumedAndReplenished(t *testing.T) {
	p := New()
	p.SetPoolMode("warm", config.IsolationPerSession, 1, 3)
	p.SetPoolWarmSpares("warm", 1)
	spawned := make(chan int, 3)
	resumed := make(chan int, 1)
	p.SetSpawnFunc(func(_ string, slotID int) { spawned <- slotID })
	p.SetResumeFunc(func(_ string, slotID int) { resumed <- slotID })
	p.ReconcileElasticWarmSpares("warm")
	<-spawned // slot 0

	if !p.BeginElasticSpareSuspend("warm", 0) {
		t.Fatal("failed to claim pristine spare for suspend")
	}
	if err := p.RegisterSuspendedElasticWorker("warm", 0, "http://127.0.0.1:9999", nil, 7); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest("GET", "/app/warm/", nil))
	if rec.Code != 200 {
		t.Fatalf("first request status = %d, want loading response", rec.Code)
	}
	select {
	case slot := <-resumed:
		if slot != 0 {
			t.Fatalf("resumed slot = %d, want 0", slot)
		}
	case <-time.After(time.Second):
		t.Fatal("resume callback not invoked")
	}
	select {
	case slot := <-spawned:
		if slot != 1 {
			t.Fatalf("replacement spare slot = %d, want 1", slot)
		}
	case <-time.After(time.Second):
		t.Fatal("consumed spare was not replenished")
	}

	snap, _ := p.ElasticWorkersSnapshot("warm")
	if snap.Workers[0].Status != "resuming" || snap.Workers[0].WarmSpare {
		t.Fatalf("consumed worker = %+v, want non-spare resuming", snap.Workers[0])
	}
	if !snap.Workers[1].WarmSpare {
		t.Fatalf("replacement worker = %+v, want warm spare", snap.Workers[1])
	}
}

func TestReconcileElasticWarmSpares_DoesNotRetireBootingProcess(t *testing.T) {
	p := New()
	p.SetPoolMode("warm", config.IsolationPerSession, 1, 2)
	p.SetPoolWarmSpares("warm", 1)
	spawned := make(chan int, 1)
	terminated := make(chan int, 1)
	p.SetSpawnFunc(func(_ string, slotID int) { spawned <- slotID })
	p.SetTerminateFunc(func(_ string, slotID int) { terminated <- slotID })
	p.ReconcileElasticWarmSpares("warm")
	slotID := <-spawned

	p.SetPoolWarmSpares("warm", 0)
	p.ReconcileElasticWarmSpares("warm")
	select {
	case got := <-terminated:
		t.Fatalf("terminated booting slot %d; lifecycle registration could race and leak it", got)
	case <-time.After(20 * time.Millisecond):
	}

	if err := p.RegisterElasticWorker("warm", slotID, "http://127.0.0.1:9999", nil, 7); err != nil {
		t.Fatal(err)
	}
	p.ReconcileElasticWarmSpares("warm")
	select {
	case got := <-terminated:
		if got != slotID {
			t.Fatalf("terminated slot = %d, want %d", got, slotID)
		}
	case <-time.After(time.Second):
		t.Fatal("ready excess spare was not retired")
	}
}
