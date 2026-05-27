package agent

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/worker"
)

// writePersistedIdentity signs a worker cert for nodeID with the given validity
// and writes the three identity files a real agent persists, so Bootstrap can be
// exercised against an already-joined data dir.
func writePersistedIdentity(t *testing.T, agentDir, nodeID string, ttl time.Duration) {
	t.Helper()
	if err := os.MkdirAll(agentDir, 0o700); err != nil {
		t.Fatal(err)
	}
	ca, err := worker.OpenCA(filepath.Join(t.TempDir(), "ca"), []string{"tok"})
	if err != nil {
		t.Fatalf("open ca: %v", err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: "shinyhub-worker"},
	}, key)
	if err != nil {
		t.Fatalf("create csr: %v", err)
	}
	csrPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})
	certPEM, err := ca.SignWorkerCSR(nodeID, csrPEM, ttl)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	for name, data := range map[string][]byte{
		"client-key.pem":  keyPEM,
		"client-cert.pem": certPEM,
		"ca-bundle.pem":   ca.CertPEM(),
	} {
		if err := os.WriteFile(filepath.Join(agentDir, name), data, 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
}

// TestBootstrap_ReadoptsValidPersistedIdentity verifies that when a still-valid
// worker identity is already on disk, Bootstrap reuses it offline: it never
// contacts the control plane to register, and recovers the node id from the
// persisted cert's SAN. The server URL is unroutable, so any Register attempt
// would fail the test.
func TestBootstrap_ReadoptsValidPersistedIdentity(t *testing.T) {
	dataDir := t.TempDir()
	agentDir := filepath.Join(dataDir, "agent")
	const nodeID = "node-readopt"
	writePersistedIdentity(t, agentDir, nodeID, time.Hour)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ag, err := Bootstrap(ctx, Config{
		ServerURL: "https://127.0.0.1:1", // unroutable: re-adoption must not dial it
		Token:     "tok",
		DataDir:   dataDir,
		Tier:      "remote",
	})
	if err != nil {
		t.Fatalf("Bootstrap re-adopt: %v", err)
	}
	if ag.NodeID() != nodeID {
		t.Errorf("NodeID = %q, want %q (must come from persisted cert SAN)", ag.NodeID(), nodeID)
	}
	if len(ag.Certs().Get().Certificate) == 0 {
		t.Error("cert holder is empty after re-adopt")
	}
}

// TestBootstrap_ReRegistersWhenPersistedCertExpired verifies that an expired
// persisted cert is not re-adopted: Bootstrap falls back to Register, which
// here fails because the server is unroutable, proving the expiry gate sent it
// down the registration path rather than reusing the dead cert.
func TestBootstrap_ReRegistersWhenPersistedCertExpired(t *testing.T) {
	dataDir := t.TempDir()
	agentDir := filepath.Join(dataDir, "agent")
	writePersistedIdentity(t, agentDir, "node-expired", -time.Hour) // NotAfter in the past

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := Bootstrap(ctx, Config{
		ServerURL: "https://127.0.0.1:1",
		Token:     "tok",
		DataDir:   dataDir,
		Tier:      "remote",
	})
	if err == nil {
		t.Fatal("expected Bootstrap to fall back to Register and fail against the unroutable server")
	}
}
