package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/worker"
	workerapi "github.com/rvben/shinyhub/internal/worker/api"
)

// countingControlPlane stands up a fake control plane that signs registrations
// and counts heartbeats, so a test can observe heartbeat timing.
func countingControlPlane(t *testing.T, heartbeats *atomic.Int32) *httptest.Server {
	t.Helper()
	ca, err := worker.OpenCA(t.TempDir(), []string{"good-token"})
	if err != nil {
		t.Fatalf("open ca: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/workers/register", func(w http.ResponseWriter, r *http.Request) {
		var req workerapi.RegisterRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		certPEM, err := ca.SignWorkerCSR("node-test", []byte(req.CSRPEM), time.Hour)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(workerapi.RegisterResponse{
			NodeID:   "node-test",
			CertPEM:  string(certPEM),
			CABundle: string(ca.CertPEM()),
		})
	})
	mux.HandleFunc("/api/workers/heartbeat", func(w http.ResponseWriter, _ *http.Request) {
		heartbeats.Add(1)
		_ = json.NewEncoder(w).Encode(workerapi.HeartbeatResponse{})
	})
	return httptest.NewServer(mux)
}

// TestRun_HeartbeatsBeforeFirstInterval verifies that Run checks in immediately
// rather than waiting a full interval. This matters when a re-adopted cert has
// little life left: waiting a 10s interval could let it expire before the first
// renewal request, stranding the worker behind an expired client cert.
func TestRun_HeartbeatsBeforeFirstInterval(t *testing.T) {
	var heartbeats atomic.Int32
	cp := countingControlPlane(t, &heartbeats)
	defer cp.Close()

	ag, err := Bootstrap(context.Background(), Config{
		ServerURL:     cp.URL,
		Token:         "good-token",
		AdvertiseAddr: "127.0.0.1:0",
		Tier:          "burst",
		DataDir:       t.TempDir(),
		Version:       "test",
	})
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	// A long interval: any heartbeat observed soon must be the up-front one, not a
	// ticker tick.
	go func() { _ = ag.Run(ctx, time.Hour); close(done) }()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && heartbeats.Load() == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done

	if got := heartbeats.Load(); got == 0 {
		t.Fatal("Run did not send a heartbeat before the first interval tick")
	}
}
