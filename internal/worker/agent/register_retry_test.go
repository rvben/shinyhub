// internal/worker/agent/register_retry_test.go
package agent

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/worker"
	workerapi "github.com/rvben/shinyhub/internal/worker/api"
)

// newCountingControlPlane is a TLS control plane (cert signed by ca) whose
// register handler returns the status produced by status(callNum) and, on 200,
// signs a real cert so Bootstrap can complete. calls counts register attempts.
func newCountingControlPlane(t *testing.T, ca *worker.CA, status func(call int32) int, calls *int32) *httptest.Server {
	t.Helper()
	serverCert, err := ca.ServerCertificate()
	if err != nil {
		t.Fatalf("server cert: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/workers/register", func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(calls, 1)
		if code := status(n); code != http.StatusOK {
			http.Error(w, "not ready", code)
			return
		}
		var req workerapi.RegisterRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		certPEM, err := ca.SignWorkerCSR("node-retry", []byte(req.CSRPEM), time.Hour)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(workerapi.RegisterResponse{
			NodeID:   "node-retry",
			CertPEM:  string(certPEM),
			CABundle: string(ca.CertPEM()),
		})
	})
	srv := httptest.NewUnstartedServer(mux)
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{serverCert}, MinVersion: tls.VersionTLS12}
	srv.StartTLS()
	return srv
}

func retryTestConfig(cp *httptest.Server, ca *worker.CA, dataDir string) Config {
	return Config{
		ServerURL:     cp.URL,
		Token:         "good-token",
		AdvertiseAddr: "127.0.0.1:18443",
		Tier:          "remote",
		DataDir:       dataDir,
		Version:       "test",
		CAPEM:         ca.CertPEM(),
	}
}

func TestRegisterWithRetry_RetriesThenSucceeds(t *testing.T) {
	ca, err := worker.OpenCA(t.TempDir(), []string{"good-token"})
	if err != nil {
		t.Fatalf("open ca: %v", err)
	}
	var calls int32
	cp := newCountingControlPlane(t, ca, func(n int32) int {
		if n == 1 {
			return http.StatusServiceUnavailable // first attempt: not yet owner
		}
		return http.StatusOK
	}, &calls)
	defer cp.Close()

	ag, err := Bootstrap(context.Background(), retryTestConfig(cp, ca, t.TempDir()))
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if ag.NodeID() != "node-retry" {
		t.Fatalf("node id = %q, want node-retry", ag.NodeID())
	}
	if got := atomic.LoadInt32(&calls); got < 2 {
		t.Fatalf("register calls = %d, want >= 2 (a 503 then a successful retry)", got)
	}
}

func TestRegisterWithRetry_FailFastOnBadToken(t *testing.T) {
	ca, err := worker.OpenCA(t.TempDir(), []string{"good-token"})
	if err != nil {
		t.Fatalf("open ca: %v", err)
	}
	var calls int32
	cp := newCountingControlPlane(t, ca, func(int32) int { return http.StatusUnauthorized }, &calls)
	defer cp.Close()

	if _, err := Bootstrap(context.Background(), retryTestConfig(cp, ca, t.TempDir())); err == nil {
		t.Fatal("expected a fatal error on 401")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("register calls = %d, want exactly 1 (no retry on 401)", got)
	}
}

func TestRegisterWithRetry_RespectsContextCancel(t *testing.T) {
	ca, err := worker.OpenCA(t.TempDir(), []string{"good-token"})
	if err != nil {
		t.Fatalf("open ca: %v", err)
	}
	var calls int32
	cp := newCountingControlPlane(t, ca, func(int32) int { return http.StatusServiceUnavailable }, &calls) // never owner
	defer cp.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err = Bootstrap(ctx, retryTestConfig(cp, ca, t.TempDir()))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded (retry must honour ctx)", err)
	}
}
