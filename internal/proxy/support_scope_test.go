package proxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/auth"
)

func TestPoolAppIDChangeFencesEveryOldBackend(t *testing.T) {
	var oldRequests atomic.Int64
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		oldRequests.Add(1)
		_, _ = w.Write([]byte("old app"))
	}))
	defer old.Close()
	p := New()
	p.SetPoolSize("sales", 1)
	p.SetPoolAppID("sales", 10)
	if err := p.RegisterReplica("sales", 0, old.URL, nil, 1); err != nil {
		t.Fatal(err)
	}
	p.SetPoolAppID("sales", 20)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/app/sales/", nil))
	if oldRequests.Load() != 0 || strings.Contains(rec.Body.String(), "old app") {
		t.Fatalf("replacement routed to old backend: requests=%d body=%s", oldRequests.Load(), rec.Body.String())
	}
}

func TestSupportSessionRejectsSelectedBackendOwnedByAnotherAppID(t *testing.T) {
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("old app secret"))
	}))
	defer old.Close()
	p := New()
	p.SetPoolSize("sales", 1)
	p.SetPoolAppID("sales", 10)
	if err := p.RegisterReplica("sales", 0, old.URL, nil, 1); err != nil {
		t.Fatal(err)
	}
	// Model a stale clustered metadata publication without using SetPoolAppID,
	// whose production implementation now fences the pool atomically.
	p.pools["sales"].appID.Store(20)
	user := &auth.ContextUser{ID: 2, Username: "alice", Role: "viewer",
		SupportSession: &auth.SupportSessionContext{ID: "support", ActorID: 1, ActorUsername: "admin",
			AppID: 20, AppSlug: "sales", ExpiresAt: time.Now().Add(15 * time.Minute)}}
	req := httptest.NewRequest(http.MethodGet, "/app/sales/", nil)
	req = req.WithContext(auth.WithUser(req.Context(), user))
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict || strings.Contains(rec.Body.String(), "old app secret") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestDelayedRegistrationCannotInheritReplacementAppID(t *testing.T) {
	p := New()
	p.SetPoolSize("sales", 1)
	p.SetPoolAppID("sales", 10)
	p.SetPoolAppID("sales", 20)
	if err := p.RegisterReplica("sales", 0, "http://127.0.0.1:19010", nil, 1, 10); err == nil {
		t.Fatal("stale app registration was accepted after slug replacement")
	}
	if got := p.ReplicaTargetURL("sales", 0); got != "" {
		t.Fatalf("stale registration changed replacement pool: %q", got)
	}
	if err := p.RegisterReplica("sales", 0, "http://127.0.0.1:19020", nil, 2, 20); err != nil {
		t.Fatalf("replacement registration failed: %v", err)
	}
}

func TestDelayedRegistrationCannotClaimFreshUnpublishedPool(t *testing.T) {
	for _, elastic := range []bool{false, true} {
		p := New()
		p.SetPoolSize("sales", 1)
		if elastic {
			p.SetPoolMode("sales", "per_session", 1, 2)
			if err := p.RegisterElasticWorker("sales", 0, "http://127.0.0.1:19010", nil, 1, 10); err == nil {
				t.Fatal("stale elastic registration claimed a fresh zero-ID pool")
			}
		} else if err := p.RegisterReplica("sales", 0, "http://127.0.0.1:19010", nil, 1, 10); err == nil {
			t.Fatal("stale fixed registration claimed a fresh zero-ID pool")
		}
	}
}

func TestElasticAppIDReplacementTerminatesOldWorkersAndIgnoresOldClose(t *testing.T) {
	p := New()
	p.SetPoolSize("sales", 1)
	p.SetPoolMode("sales", "per_session", 1, 2)
	p.SetPoolAppID("sales", 10)
	oldClient := &clientSlot{slotID: 0, liveConns: 1}
	p.mu.Lock()
	p.pools["sales"].workers[0] = &replicaBackend{slotID: 0, ownerAppID: 10, status: workerRunning, assignedClients: 1}
	p.clients["sales"] = map[string]*clientSlot{"browser": oldClient}
	p.mu.Unlock()
	terminated := make(chan int, 1)
	p.SetTerminateFunc(func(_ string, slotID int) { terminated <- slotID })

	p.SetPoolAppID("sales", 20)
	select {
	case slotID := <-terminated:
		if slotID != 0 {
			t.Fatalf("terminated slot = %d", slotID)
		}
	case <-time.After(time.Second):
		t.Fatal("old elastic worker was not terminated")
	}

	newClient := &clientSlot{slotID: 0, liveConns: 1}
	p.mu.Lock()
	p.pools["sales"].workers[0] = &replicaBackend{slotID: 0, ownerAppID: 20, status: workerRunning, assignedClients: 1}
	p.clients["sales"] = map[string]*clientSlot{"browser": newClient}
	p.mu.Unlock()
	p.clientConnClosed("sales", "browser", oldClient)
	if newClient.liveConns != 1 {
		t.Fatalf("old request close changed replacement binding: liveConns=%d", newClient.liveConns)
	}
}
