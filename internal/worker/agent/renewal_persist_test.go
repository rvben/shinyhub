// internal/worker/agent/renewal_persist_test.go
package agent

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/worker"
)

// renewalTestIdentity builds a worker keypair plus two certs signed off the same
// CSR (so both verify against the retained private key, matching how a real
// renewal resubmits the same CSR): old is what the holder starts with, new is
// what applyRenewedCert is asked to adopt.
func renewalTestIdentity(t *testing.T) (ca *worker.CA, keyPEM, oldCertPEM, newCertPEM []byte, oldCert tls.Certificate) {
	t.Helper()
	ca, err := worker.OpenCA(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("open ca: %v", err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	csrPEM, err := csrPEMFromKey(key)
	if err != nil {
		t.Fatalf("csr: %v", err)
	}
	oldCertPEM, err = ca.SignWorkerCSR("node-x", csrPEM, time.Hour)
	if err != nil {
		t.Fatalf("sign old cert: %v", err)
	}
	newCertPEM, err = ca.SignWorkerCSR("node-x", csrPEM, time.Hour)
	if err != nil {
		t.Fatalf("sign new cert: %v", err)
	}
	oldCert, err = tls.X509KeyPair(oldCertPEM, keyPEM)
	if err != nil {
		t.Fatalf("old keypair: %v", err)
	}
	return ca, keyPEM, oldCertPEM, newCertPEM, oldCert
}

// TestApplyRenewedCert_PersistFailureLeavesMemoryAndDiskUnchanged verifies that
// a renewed certificate is never adopted in the holder unless it was durably
// persisted first. Today applyRenewedCert swaps the holder and only then writes
// the file, so a write failure leaves the agent presenting a certificate that
// exists nowhere on disk: a restart loses it, and because the swap already
// reported success the next heartbeat has no reason to retry. A failed persist
// must leave both the holder and the on-disk file exactly as they were, so the
// next heartbeat still sees renewal as due.
//
// The failure is injected via createTempHook rather than a filesystem
// permission trick: persistAtomically commits through a rename, which does
// not consult the destination file's own permission bits, so a chmod on the
// target file fails a naive in-place os.WriteFile but not an atomic
// temp-file-plus-rename write. The hook fails deterministically regardless of
// which write strategy is under test.
func TestApplyRenewedCert_PersistFailureLeavesMemoryAndDiskUnchanged(t *testing.T) {
	_, keyPEM, oldCertPEM, newCertPEM, oldCert := renewalTestIdentity(t)

	dataDir := t.TempDir()
	agentDir := filepath.Join(dataDir, "agent")
	if err := os.MkdirAll(agentDir, 0o700); err != nil {
		t.Fatal(err)
	}
	certPath := filepath.Join(agentDir, "client-cert.pem")
	if err := os.WriteFile(certPath, oldCertPEM, 0o600); err != nil {
		t.Fatal(err)
	}

	prevCreateTemp := createTempHook
	createTempHook = func(string, string) (*os.File, error) {
		return nil, errors.New("simulated disk full")
	}
	t.Cleanup(func() { createTempHook = prevCreateTemp })

	a := &Agent{cfg: Config{DataDir: dataDir}, nodeID: "node-x", certs: worker.NewCertHolder(oldCert), keyPEM: keyPEM}

	if err := a.applyRenewedCert(string(newCertPEM)); err == nil {
		t.Fatal("applyRenewedCert succeeded despite an unwritable data dir")
	}

	got := a.certs.Get()
	if len(got.Certificate) == 0 || !bytes.Equal(got.Certificate[0], oldCert.Certificate[0]) {
		t.Error("holder was swapped to the renewed cert despite the failed persist")
	}

	onDisk, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatalf("read cert file: %v", err)
	}
	if !bytes.Equal(onDisk, oldCertPEM) {
		t.Errorf("on-disk cert changed despite the failed persist: got %d bytes, want the original %d bytes unchanged", len(onDisk), len(oldCertPEM))
	}
}

// TestApplyRenewedCert_DirSyncFailureStillSwapsAndWarns verifies that once a
// renewed cert has been durably renamed into place, a failure of the trailing
// parent-directory fsync is only a durability warning, not a failed renewal:
// the content already sits safely under its final name, so the holder still
// adopts it, the file on disk carries the new content, and the failure is
// logged rather than silently dropped or treated as a hard error.
func TestApplyRenewedCert_DirSyncFailureStillSwapsAndWarns(t *testing.T) {
	_, keyPEM, oldCertPEM, newCertPEM, oldCert := renewalTestIdentity(t)

	dataDir := t.TempDir()
	agentDir := filepath.Join(dataDir, "agent")
	if err := os.MkdirAll(agentDir, 0o700); err != nil {
		t.Fatal(err)
	}
	certPath := filepath.Join(agentDir, "client-cert.pem")
	if err := os.WriteFile(certPath, oldCertPEM, 0o600); err != nil {
		t.Fatal(err)
	}

	prevHook := syncDirHook
	syncDirHook = func(string) error { return errors.New("simulated dir fsync failure") }
	t.Cleanup(func() { syncDirHook = prevHook })

	var mu sync.Mutex
	var recs []slog.Record
	prevLogger := slog.Default()
	slog.SetDefault(slog.New(captureHandler{mu: &mu, recs: &recs}))
	t.Cleanup(func() { slog.SetDefault(prevLogger) })

	a := &Agent{cfg: Config{DataDir: dataDir}, nodeID: "node-x", certs: worker.NewCertHolder(oldCert), keyPEM: keyPEM}

	if err := a.applyRenewedCert(string(newCertPEM)); err != nil {
		t.Fatalf("applyRenewedCert: %v", err)
	}

	newLeaf, err := tls.X509KeyPair(newCertPEM, keyPEM)
	if err != nil {
		t.Fatalf("parse new cert: %v", err)
	}
	got := a.certs.Get()
	if len(got.Certificate) == 0 || !bytes.Equal(got.Certificate[0], newLeaf.Certificate[0]) {
		t.Error("holder was not swapped to the renewed cert despite a successful rename")
	}

	onDisk, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatalf("read cert file: %v", err)
	}
	if !bytes.Equal(onDisk, newCertPEM) {
		t.Error("on-disk cert was not updated to the renewed cert despite a successful rename")
	}

	mu.Lock()
	defer mu.Unlock()
	found := false
	for _, r := range recs {
		if r.Level == slog.LevelWarn {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a warning log for the failed directory fsync, got %d records", len(recs))
	}
}
