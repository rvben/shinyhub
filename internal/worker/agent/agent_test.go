// internal/worker/agent/agent_test.go
package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/worker"
	workerapi "github.com/rvben/shinyhub/internal/worker/api"
)

func TestAgentBootstrapStoresIdentity(t *testing.T) {
	// A fake control plane that signs whatever CSR it receives.
	cp := newFakeControlPlane(t)
	defer cp.Close()

	dataDir := t.TempDir()
	ag, err := Bootstrap(context.Background(), Config{
		ServerURL:     cp.URL,
		Token:         "good-token",
		AdvertiseAddr: "127.0.0.1:18443",
		Tier:          "burst",
		DataDir:       dataDir,
		Version:       "test",
	})
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if ag.NodeID() == "" {
		t.Fatal("agent has no node id after bootstrap")
	}

	// Verify all three identity files were persisted with correct permissions.
	for _, name := range []string{"client-key.pem", "client-cert.pem", "ca-bundle.pem"} {
		path := filepath.Join(dataDir, "agent", name)
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatalf("identity file %s: %v", name, err)
		}
		if fi.Size() == 0 {
			t.Fatalf("identity file %s is empty", name)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("identity file %s has perm %04o, want 0600", name, fi.Mode().Perm())
		}
	}

	// A heartbeat round trip succeeds using the mTLS client built from the issued cert.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := ag.heartbeatOnce(ctx); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
}

// TestAgent_FencedResponse_StopsAllAndAdopts pins the worker-side crux of
// fencing: a heartbeat response with Fenced=true must invoke OnFenced (which
// the boot wiring binds to replicaServer.StopAll, killing every replica this
// node still runs after being reaped) and adopt the control plane's new
// incarnation so the next heartbeat matches and re-ups the worker clean.
func TestAgent_FencedResponse_StopsAllAndAdopts(t *testing.T) {
	cp := fencedControlPlane(t)
	defer cp.Close()

	ag, err := Bootstrap(context.Background(), Config{
		ServerURL:     cp.URL,
		Token:         "good-token",
		AdvertiseAddr: "127.0.0.1:18445",
		Tier:          "burst",
		DataDir:       t.TempDir(),
		Version:       "test",
	})
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	stopped := false
	ag.OnFenced = func() { stopped = true }
	ag.incarnation = 1

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := ag.heartbeatOnce(ctx); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if !stopped {
		t.Fatal("a fenced response must trigger OnFenced (StopAll)")
	}
	if ag.incarnation != 2 {
		t.Fatalf("agent must adopt the new incarnation: got %d want 2", ag.incarnation)
	}
}

// fencedControlPlane is a fake control plane whose heartbeat handler always
// responds Fenced with a new incarnation, so a test can drive heartbeatOnce
// through the fencing path without a real registry.
func fencedControlPlane(t *testing.T) *httptest.Server {
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
		if !ca.VerifyJoinToken(req.Token) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		certPEM, err := ca.SignWorkerCSR("node-fenced", []byte(req.CSRPEM), time.Hour)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(workerapi.RegisterResponse{
			NodeID:   "node-fenced",
			CertPEM:  string(certPEM),
			CABundle: string(ca.CertPEM()),
		})
	})
	mux.HandleFunc("/api/workers/heartbeat", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(workerapi.HeartbeatResponse{Fenced: true, Incarnation: 2})
	})
	return httptest.NewServer(mux)
}

func newFakeControlPlane(t *testing.T) *httptest.Server {
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
		if !ca.VerifyJoinToken(req.Token) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		nodeID := "node-test"
		certPEM, err := ca.SignWorkerCSR(nodeID, []byte(req.CSRPEM), time.Hour)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(workerapi.RegisterResponse{
			NodeID:   nodeID,
			CertPEM:  string(certPEM),
			CABundle: string(ca.CertPEM()),
		})
	})
	mux.HandleFunc("/api/workers/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return httptest.NewServer(mux)
}
