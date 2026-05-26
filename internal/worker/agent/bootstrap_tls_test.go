// internal/worker/agent/bootstrap_tls_test.go
package agent

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/worker"
	workerapi "github.com/rvben/shinyhub/internal/worker/api"
)

// newTLSControlPlane wraps a register/heartbeat mux in an HTTPS server whose
// leaf certificate is signed by ca, mirroring the production control plane's
// self-signed worker-facing listener (serveWorkerMTLS). It does not require a
// client certificate, since workers have none until register completes.
func newTLSControlPlane(t *testing.T, ca *worker.CA) *httptest.Server {
	t.Helper()
	serverCert, err := ca.ServerCertificate()
	if err != nil {
		t.Fatalf("server cert: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/workers/register", func(w http.ResponseWriter, r *http.Request) {
		var req workerapi.RegisterRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if !ca.VerifyJoinToken(req.Token) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		certPEM, err := ca.SignWorkerCSR("node-tls", []byte(req.CSRPEM), time.Hour)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(workerapi.RegisterResponse{
			NodeID:   "node-tls",
			CertPEM:  string(certPEM),
			CABundle: string(ca.CertPEM()),
		})
	})
	srv := httptest.NewUnstartedServer(mux)
	srv.TLS = &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		MinVersion:   tls.VersionTLS12,
	}
	srv.StartTLS()
	return srv
}

// TestBootstrapPinsCA verifies that Bootstrap can join a control plane that
// serves over a self-signed-CA TLS cert only when the CA is provided, and that
// without it the handshake fails against system roots.
func TestBootstrapPinsCA(t *testing.T) {
	ca, err := worker.OpenCA(t.TempDir(), []string{"good-token"})
	if err != nil {
		t.Fatalf("open ca: %v", err)
	}
	cp := newTLSControlPlane(t, ca)
	defer cp.Close()

	// Without the CA, verification falls back to system roots and must fail.
	if _, err := Bootstrap(context.Background(), Config{
		ServerURL:     cp.URL,
		Token:         "good-token",
		AdvertiseAddr: "127.0.0.1:18443",
		Tier:          "remote",
		DataDir:       t.TempDir(),
		Version:       "test",
	}); err == nil {
		t.Fatal("expected bootstrap to fail without pinned CA, but it succeeded")
	}

	// With the CA pinned, the join handshake succeeds.
	ag, err := Bootstrap(context.Background(), Config{
		ServerURL:     cp.URL,
		Token:         "good-token",
		AdvertiseAddr: "127.0.0.1:18443",
		Tier:          "remote",
		DataDir:       t.TempDir(),
		Version:       "test",
		CAPEM:         ca.CertPEM(),
	})
	if err != nil {
		t.Fatalf("bootstrap with pinned CA: %v", err)
	}
	if ag.NodeID() != "node-tls" {
		t.Fatalf("node id = %q, want node-tls", ag.NodeID())
	}
}
