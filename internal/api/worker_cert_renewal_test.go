package api

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/worker"
	"github.com/rvben/shinyhub/internal/worker/agent"
)

func newRenewalTestStore(t *testing.T) *db.Store {
	t.Helper()
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return store
}

// freePort binds an ephemeral port, releases it, and returns the address so the
// worker can be registered with the same advertise address it will later bind.
func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("probe port: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

// TestWorkerCertRenewal_RoutingSurvivesPastOriginalExpiry drives the full
// production worker path: a worker joins (cert TTL deliberately short), runs its
// heartbeat loop against the real WorkerAPI, and serves its inbound mTLS API.
// After the original certificate's expiry, the control plane must still be able
// to reach the worker's data plane over mTLS. Without heartbeat-driven renewal
// and TLS hot-reload, the worker's server cert expires and every dial fails,
// taking the tier's routing surface down. The test fails until renewal is wired.
func TestWorkerCertRenewal_RoutingSurvivesPastOriginalExpiry(t *testing.T) {
	store := newRenewalTestStore(t)
	ca, err := worker.OpenCA(t.TempDir(), []string{"tok"})
	if err != nil {
		t.Fatalf("open ca: %v", err)
	}
	reg, err := worker.NewRegistry(store)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}

	wapi := NewWorkerAPI(store, reg, ca, "")
	// Short TTL so the test exercises a full expiry-and-renewal cycle in seconds.
	wapi.certTTL = 2 * time.Second

	// Control-plane worker-facing mTLS listener (mirrors serveWorkerMTLS).
	cpCert, err := ca.ServerCertificate("127.0.0.1")
	if err != nil {
		t.Fatalf("cp server cert: %v", err)
	}
	r := chi.NewRouter()
	r.Post("/api/workers/register", wapi.HandleRegister)
	r.Post("/api/workers/heartbeat", wapi.HandleHeartbeat)
	cp := httptest.NewUnstartedServer(r)
	cp.TLS = &tls.Config{
		Certificates: []tls.Certificate{cpCert},
		ClientAuth:   tls.VerifyClientCertIfGiven,
		ClientCAs:    ca.Pool(),
		MinVersion:   tls.VersionTLS12,
	}
	cp.StartTLS()
	defer cp.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	agentAddr := freePort(t)
	ag, err := agent.Bootstrap(ctx, agent.Config{
		ServerURL:     cp.URL,
		Token:         "tok",
		AdvertiseAddr: agentAddr,
		Tier:          "remote",
		DataDir:       t.TempDir(),
		Version:       "test",
		CAPEM:         ca.CertPEM(),
	})
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	// Stand up the worker's inbound mTLS server on its advertised address,
	// reading its server cert through the agent's holder so renewal hot-reloads.
	agentSrv := worker.NewAgentServer(worker.AgentServerConfig{
		CertSource: ag.Certs(),
		ClientCAs:  ag.CAPool(),
		NodeID:     ag.NodeID(),
	})
	ln, err := tls.Listen("tcp", agentAddr, agentSrv.TLSConfig())
	if err != nil {
		t.Fatalf("agent listen: %v", err)
	}
	go func() { _ = agentSrv.ServeListener(ctx, ln) }()
	// The heartbeat loop is what drives certificate renewal.
	go func() { _ = ag.Run(ctx, 150*time.Millisecond) }()

	// Capture the original cert's expiry before renewal can extend it.
	leaf, err := x509.ParseCertificate(ag.Certs().Get().Certificate[0])
	if err != nil {
		t.Fatalf("parse issued cert: %v", err)
	}
	originalNotAfter := leaf.NotAfter

	dialer, err := worker.NewMTLSDialer(ca.ControlClientCertificate, ca.Pool())
	if err != nil {
		t.Fatalf("control client cert: %v", err)
	}

	// Wait until the original cert has certainly expired, then require the
	// control plane to still reach the worker (only possible if it renewed).
	time.Sleep(time.Until(originalNotAfter) + 300*time.Millisecond)

	deadline := time.Now().Add(2 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		w, ok := reg.WorkerForTier("remote")
		if !ok {
			lastErr = fmt.Errorf("no live worker for tier")
			time.Sleep(100 * time.Millisecond)
			continue
		}
		client, base, err := dialer.DialWorker(w)
		if err != nil {
			lastErr = err
			time.Sleep(100 * time.Millisecond)
			continue
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/inventory", nil)
		resp, err := client.Do(req)
		if err != nil {
			// A TLS handshake failure here is the expired-cert symptom.
			lastErr = err
			time.Sleep(100 * time.Millisecond)
			continue
		}
		_ = resp.Body.Close()
		// Any HTTP response means the mTLS handshake succeeded: routing is alive
		// past the original cert expiry, so renewal + hot-reload worked.
		return
	}
	t.Fatalf("control plane could not reach worker past original cert expiry (%s): %v", originalNotAfter, lastErr)
}
