package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
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

// TestRun_DoesNotHeartbeatWhenListenerFailsToBind verifies that if binding the
// inbound listener fails (e.g. the advertised port is already in use), Run
// surfaces that error without first heartbeating, so the control plane is never
// told a worker is up while its serving side could not even bind. Because Listen
// runs synchronously before the up-front heartbeat, this is deterministic: the
// bind error is observed before any liveness check, with no timing window.
func TestRun_DoesNotHeartbeatWhenListenerFailsToBind(t *testing.T) {
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
	ag.Listen = func() (net.Listener, error) {
		return nil, errors.New("listen tcp 127.0.0.1:0: bind: address already in use")
	}
	ag.Serve = func(context.Context, net.Listener) error {
		t.Error("Serve was called even though Listen failed to bind")
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	runErr := ag.Run(ctx, time.Hour)

	if runErr == nil || !strings.Contains(runErr.Error(), "agent server") {
		t.Fatalf("Run err = %v, want an agent server startup error", runErr)
	}
	if got := heartbeats.Load(); got != 0 {
		t.Errorf("worker heartbeated %d times despite the listener failing to bind", got)
	}
}

// TestRun_ServesBoundListenerAndHeartbeats verifies the happy path: once Listen
// binds successfully, Run hands that exact listener to Serve and heartbeats up
// front. Binding synchronously before the heartbeat means the port is already
// held when the worker announces itself up.
func TestRun_ServesBoundListenerAndHeartbeats(t *testing.T) {
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
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	// serveStarted is closed once Serve runs, so the test can synchronize on the
	// goroutine actually executing before asserting, rather than racing it.
	serveStarted := make(chan struct{})
	var gotExpectedLn atomic.Bool
	ag.Listen = func() (net.Listener, error) { return ln, nil }
	ag.Serve = func(ctx context.Context, l net.Listener) error {
		gotExpectedLn.Store(l == ln)
		close(serveStarted)
		<-ctx.Done()
		return ctx.Err()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { _ = ag.Run(ctx, time.Hour); close(done) }()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && heartbeats.Load() == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case <-serveStarted:
	case <-time.After(time.Second):
		t.Fatal("Serve was not called after Listen bound the listener")
	}
	cancel()
	<-done

	if !gotExpectedLn.Load() {
		t.Error("Serve did not receive the listener returned by Listen")
	}
	if got := heartbeats.Load(); got == 0 {
		t.Error("Run did not heartbeat after binding the listener")
	}
}
