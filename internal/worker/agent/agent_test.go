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
