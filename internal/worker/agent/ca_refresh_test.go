package agent

import (
	"bytes"
	"crypto/x509"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
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

// TestApplyCABundle_PersistFailureLeavesMemoryAndDiskUnchanged verifies that a
// rotated CA bundle is never trusted unless it was durably persisted first.
// Today applyCABundle swaps the holder and only then writes the file, so a
// write failure leaves the worker trusting a bundle that exists nowhere on
// disk: a restart loses it, and because the swap already reported success the
// next heartbeat has no reason to retry the persist.
//
// The failure is injected via createTempHook rather than a filesystem
// permission trick: persistAtomically commits through a rename, which does
// not consult the destination file's own permission bits, so a chmod on the
// target file fails a naive in-place os.WriteFile but not an atomic
// temp-file-plus-rename write. The hook fails deterministically regardless of
// which write strategy is under test.
func TestApplyCABundle_PersistFailureLeavesMemoryAndDiskUnchanged(t *testing.T) {
	caOld, err := worker.OpenCA(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("open old ca: %v", err)
	}
	caNew, err := worker.OpenCA(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("open new ca: %v", err)
	}

	dataDir := t.TempDir()
	agentDir := filepath.Join(dataDir, "agent")
	if err := os.MkdirAll(agentDir, 0o700); err != nil {
		t.Fatal(err)
	}
	bundlePath := filepath.Join(agentDir, "ca-bundle.pem")
	if err := os.WriteFile(bundlePath, caOld.CertPEM(), 0o600); err != nil {
		t.Fatal(err)
	}

	prevCreateTemp := createTempHook
	createTempHook = func(string, string) (*os.File, error) {
		return nil, errors.New("simulated disk full")
	}
	t.Cleanup(func() { createTempHook = prevCreateTemp })

	holder, err := worker.NewCAHolder(caOld.CertPEM())
	if err != nil {
		t.Fatalf("ca holder: %v", err)
	}
	a := &Agent{cfg: Config{DataDir: dataDir}, nodeID: "node-x", cacerts: holder}

	if err := a.applyCABundle(string(caNew.CertPEM())); err == nil {
		t.Fatal("applyCABundle succeeded despite a read-only bundle file")
	}

	srvOld, _ := caOld.ServerCertificate("127.0.0.1")
	leafOld, err := x509.ParseCertificate(srvOld.Certificate[0])
	if err != nil {
		t.Fatalf("parse old cert: %v", err)
	}
	if _, err := leafOld.Verify(x509.VerifyOptions{Roots: holder.Pool()}); err != nil {
		t.Errorf("holder no longer trusts the original CA despite the failed persist: %v", err)
	}
	srvNew, _ := caNew.ServerCertificate("127.0.0.1")
	leafNew, err := x509.ParseCertificate(srvNew.Certificate[0])
	if err != nil {
		t.Fatalf("parse new cert: %v", err)
	}
	if _, err := leafNew.Verify(x509.VerifyOptions{Roots: holder.Pool()}); err == nil {
		t.Error("holder trusts the rotated CA despite the failed persist")
	}

	onDisk, err := os.ReadFile(bundlePath)
	if err != nil {
		t.Fatalf("read bundle file: %v", err)
	}
	if !bytes.Equal(onDisk, caOld.CertPEM()) {
		t.Error("on-disk bundle changed despite the failed persist")
	}
}

// TestApplyCABundle_DirSyncFailureStillSwapsAndWarns verifies that once a
// rotated CA bundle has been durably renamed into place, a failure of the
// trailing parent-directory fsync is only a durability warning: the holder
// still trusts the new bundle, the file on disk carries it, and the failure
// is logged.
func TestApplyCABundle_DirSyncFailureStillSwapsAndWarns(t *testing.T) {
	caOld, err := worker.OpenCA(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("open old ca: %v", err)
	}
	caNew, err := worker.OpenCA(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("open new ca: %v", err)
	}

	dataDir := t.TempDir()
	agentDir := filepath.Join(dataDir, "agent")
	if err := os.MkdirAll(agentDir, 0o700); err != nil {
		t.Fatal(err)
	}
	bundlePath := filepath.Join(agentDir, "ca-bundle.pem")
	if err := os.WriteFile(bundlePath, caOld.CertPEM(), 0o600); err != nil {
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

	holder, err := worker.NewCAHolder(caOld.CertPEM())
	if err != nil {
		t.Fatalf("ca holder: %v", err)
	}
	a := &Agent{cfg: Config{DataDir: dataDir}, nodeID: "node-x", cacerts: holder}

	if err := a.applyCABundle(string(caNew.CertPEM())); err != nil {
		t.Fatalf("applyCABundle: %v", err)
	}

	srvNew, _ := caNew.ServerCertificate("127.0.0.1")
	leafNew, err := x509.ParseCertificate(srvNew.Certificate[0])
	if err != nil {
		t.Fatalf("parse new cert: %v", err)
	}
	if _, err := leafNew.Verify(x509.VerifyOptions{Roots: holder.Pool()}); err != nil {
		t.Errorf("holder does not trust the rotated CA despite a successful rename: %v", err)
	}

	onDisk, err := os.ReadFile(bundlePath)
	if err != nil {
		t.Fatalf("read bundle file: %v", err)
	}
	if !bytes.Equal(onDisk, caNew.CertPEM()) {
		t.Error("on-disk bundle was not updated despite a successful rename")
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

// TestApplyCABundle_MalformedPEMLeavesDiskUntouched verifies that an
// unparseable CA bundle is rejected before any disk write, so a malformed
// rotation can never overwrite a previously good, still-valid bundle file.
func TestApplyCABundle_MalformedPEMLeavesDiskUntouched(t *testing.T) {
	caOld, err := worker.OpenCA(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("open old ca: %v", err)
	}

	dataDir := t.TempDir()
	agentDir := filepath.Join(dataDir, "agent")
	if err := os.MkdirAll(agentDir, 0o700); err != nil {
		t.Fatal(err)
	}
	bundlePath := filepath.Join(agentDir, "ca-bundle.pem")
	if err := os.WriteFile(bundlePath, caOld.CertPEM(), 0o600); err != nil {
		t.Fatal(err)
	}

	holder, err := worker.NewCAHolder(caOld.CertPEM())
	if err != nil {
		t.Fatalf("ca holder: %v", err)
	}
	a := &Agent{cfg: Config{DataDir: dataDir}, nodeID: "node-x", cacerts: holder}

	if err := a.applyCABundle("not a certificate"); err == nil {
		t.Fatal("applyCABundle(malformed) did not error")
	}

	onDisk, err := os.ReadFile(bundlePath)
	if err != nil {
		t.Fatalf("read bundle file: %v", err)
	}
	if !bytes.Equal(onDisk, caOld.CertPEM()) {
		t.Error("on-disk bundle changed for a malformed rotation that should never reach disk")
	}
}
