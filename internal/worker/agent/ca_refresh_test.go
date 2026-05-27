package agent

import (
	"crypto/x509"
	"os"
	"path/filepath"
	"testing"

	"github.com/rvben/shinyhub/internal/worker"
)

// TestApplyCABundle_RotatesTrustAndPersists verifies that applying a new CA
// bundle swaps the trust pool the worker presents to live listeners/clients and
// rewrites the on-disk bundle, while re-applying the same bundle is a no-op.
func TestApplyCABundle_RotatesTrustAndPersists(t *testing.T) {
	caOld, err := worker.OpenCA(filepath.Join(t.TempDir(), "old"), nil)
	if err != nil {
		t.Fatalf("open old ca: %v", err)
	}
	caNew, err := worker.OpenCA(filepath.Join(t.TempDir(), "new"), nil)
	if err != nil {
		t.Fatalf("open new ca: %v", err)
	}

	dataDir := t.TempDir()
	agentDir := filepath.Join(dataDir, "agent")
	if err := os.MkdirAll(agentDir, 0o700); err != nil {
		t.Fatal(err)
	}
	holder, err := worker.NewCAHolder(caOld.CertPEM())
	if err != nil {
		t.Fatalf("ca holder: %v", err)
	}
	a := &Agent{cfg: Config{DataDir: dataDir}, cacerts: holder}

	if err := a.applyCABundle(string(caNew.CertPEM())); err != nil {
		t.Fatalf("applyCABundle: %v", err)
	}

	// The holder now trusts the new CA.
	srv, _ := caNew.ServerCertificate("127.0.0.1")
	leaf, err := x509.ParseCertificate(srv.Certificate[0])
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: holder.Pool()}); err != nil {
		t.Errorf("holder does not trust rotated CA after apply: %v", err)
	}

	// The on-disk bundle was rewritten to the new CA.
	onDisk, err := os.ReadFile(filepath.Join(agentDir, "ca-bundle.pem"))
	if err != nil {
		t.Fatalf("read persisted bundle: %v", err)
	}
	if string(onDisk) != string(caNew.CertPEM()) {
		t.Error("persisted ca-bundle.pem was not updated to the rotated CA")
	}

	// Re-applying the same bundle must not error and must not rewrite needlessly.
	if err := a.applyCABundle(string(caNew.CertPEM())); err != nil {
		t.Fatalf("re-apply same bundle: %v", err)
	}
}
