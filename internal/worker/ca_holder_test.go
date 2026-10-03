package worker

import (
	"crypto/x509"
	"errors"
	"sync"
	"testing"
	"time"
)

// TestCAHolder_SetRotatesTrust verifies that the holder ignores a re-set of the
// same bundle, swaps trust when a different bundle arrives, and rejects an
// unparseable bundle without disturbing the current pool.
func TestCAHolder_SetRotatesTrust(t *testing.T) {
	ca1, err := OpenCA(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("open ca1: %v", err)
	}
	ca2, err := OpenCA(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("open ca2: %v", err)
	}

	h, err := NewCAHolder(ca1.CertPEM())
	if err != nil {
		t.Fatalf("new holder: %v", err)
	}

	if changed, err := h.Set(ca1.CertPEM()); err != nil || changed {
		t.Errorf("Set(same) = (changed=%v, err=%v), want (false, nil)", changed, err)
	}

	changed, err := h.Set(ca2.CertPEM())
	if err != nil {
		t.Fatalf("Set(ca2): %v", err)
	}
	if !changed {
		t.Fatal("Set(ca2) reported no change")
	}

	// The rotated pool must trust a ca2-signed cert and no longer build a chain
	// for a ca1-signed one.
	srv2, _ := ca2.ServerCertificate("127.0.0.1")
	leaf2, _ := x509.ParseCertificate(srv2.Certificate[0])
	if _, err := leaf2.Verify(x509.VerifyOptions{Roots: h.Pool()}); err != nil {
		t.Errorf("rotated pool rejects ca2 cert: %v", err)
	}
	srv1, _ := ca1.ServerCertificate("127.0.0.1")
	leaf1, _ := x509.ParseCertificate(srv1.Certificate[0])
	if _, err := leaf1.Verify(x509.VerifyOptions{Roots: h.Pool()}); err == nil {
		t.Error("rotated pool still trusts the superseded ca1 cert")
	}

	if _, err := h.Set([]byte("-----BEGIN CERTIFICATE-----\nnot base64\n-----END CERTIFICATE-----")); err == nil {
		t.Error("Set(invalid) did not error")
	}
	// The invalid Set must not have disturbed the ca2 pool.
	if _, err := leaf2.Verify(x509.VerifyOptions{Roots: h.Pool()}); err != nil {
		t.Errorf("pool changed after rejected Set: %v", err)
	}
}

// TestCAHolder_SetPersisted_PersistFailureLeavesMemoryUnchanged verifies that a
// failing persist callback leaves the pool trusting only the original bundle.
// SetPersisted must validate and persist before it swaps the in-memory pool, so
// a caller whose disk write fails never ends up trusting a bundle that exists
// nowhere on disk.
func TestCAHolder_SetPersisted_PersistFailureLeavesMemoryUnchanged(t *testing.T) {
	caOld, err := OpenCA(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("open old ca: %v", err)
	}
	caNew, err := OpenCA(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("open new ca: %v", err)
	}
	h, err := NewCAHolder(caOld.CertPEM())
	if err != nil {
		t.Fatalf("new holder: %v", err)
	}

	_, err = h.SetPersisted(caNew.CertPEM(), func([]byte) error {
		return errors.New("simulated disk full")
	})
	if err == nil {
		t.Fatal("SetPersisted succeeded despite a failing persist callback")
	}

	srvOld, _ := caOld.ServerCertificate("127.0.0.1")
	leafOld, _ := x509.ParseCertificate(srvOld.Certificate[0])
	if _, err := leafOld.Verify(x509.VerifyOptions{Roots: h.Pool()}); err != nil {
		t.Errorf("pool no longer trusts the original CA despite the failed persist: %v", err)
	}
	srvNew, _ := caNew.ServerCertificate("127.0.0.1")
	leafNew, _ := x509.ParseCertificate(srvNew.Certificate[0])
	if _, err := leafNew.Verify(x509.VerifyOptions{Roots: h.Pool()}); err == nil {
		t.Error("pool trusts the rotated CA despite the failed persist")
	}
}

// TestCAHolder_SetPersisted_MalformedPEMNeverPersists verifies that an
// unparseable bundle is rejected before the persist callback ever runs, so a
// malformed rotation can never reach disk.
func TestCAHolder_SetPersisted_MalformedPEMNeverPersists(t *testing.T) {
	caOld, err := OpenCA(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("open old ca: %v", err)
	}
	h, err := NewCAHolder(caOld.CertPEM())
	if err != nil {
		t.Fatalf("new holder: %v", err)
	}

	var persistCalled bool
	_, err = h.SetPersisted([]byte("not a certificate"), func([]byte) error {
		persistCalled = true
		return nil
	})
	if err == nil {
		t.Fatal("SetPersisted(malformed) did not error")
	}
	if persistCalled {
		t.Error("persist callback was invoked for a malformed bundle that should never reach disk")
	}
}

// TestCAHolder_SetPersisted_PoolNotBlockedDuringPersist verifies that Pool()
// (read on every TLS handshake) never blocks behind a slow persist callback.
// SetPersisted must not hold the holder's read-write lock while it calls
// persist; it should only take the lock briefly for the final in-memory swap.
//
// This property already holds by accident in a naive reorder that calls
// persist outside of any lock, so it has no meaningful failing-first state
// against this repository's prior buggy code (which never even reaches this
// call shape). It is verified here by mutation-check: confirmed to pass
// against the real fix, and confirmed to fail when the implementation is
// temporarily mutated to hold the write lock across persist.
func TestCAHolder_SetPersisted_PoolNotBlockedDuringPersist(t *testing.T) {
	caOld, err := OpenCA(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("open old ca: %v", err)
	}
	caNew, err := OpenCA(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("open new ca: %v", err)
	}
	h, err := NewCAHolder(caOld.CertPEM())
	if err != nil {
		t.Fatalf("new holder: %v", err)
	}

	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := h.SetPersisted(caNew.CertPEM(), func([]byte) error {
			close(entered)
			<-release
			return nil
		})
		done <- err
	}()

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("persist callback was never entered")
	}

	// While persist is blocked mid-call, Pool() must still return promptly.
	poolDone := make(chan struct{})
	go func() {
		h.Pool()
		close(poolDone)
	}()
	select {
	case <-poolDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Pool() blocked while a persist callback was in flight; SetPersisted must not hold the holder lock during disk I/O")
	}

	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("SetPersisted: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SetPersisted never returned after persist was released")
	}
}

// TestCAHolder_SetPersisted_ConcurrentCallsSerializePersist verifies that
// concurrent SetPersisted calls never run their persist callbacks at the same
// time. Two writers racing to write the same on-disk bundle file must not
// interleave, so SetPersisted needs its own write-serializing lock distinct
// from the pool's read-write lock (which stays free for concurrent Pool()
// reads).
//
// This is testable failing-first: with no write-serializing lock, two persist
// callbacks launched concurrently are free to run their (deliberately slow)
// bodies at the same time.
func TestCAHolder_SetPersisted_ConcurrentCallsSerializePersist(t *testing.T) {
	base, err := OpenCA(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("open base ca: %v", err)
	}
	caA, err := OpenCA(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("open ca a: %v", err)
	}
	caB, err := OpenCA(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("open ca b: %v", err)
	}
	h, err := NewCAHolder(base.CertPEM())
	if err != nil {
		t.Fatalf("new holder: %v", err)
	}

	var mu sync.Mutex
	var intervals [][2]time.Time
	record := func([]byte) error {
		start := time.Now()
		time.Sleep(50 * time.Millisecond)
		end := time.Now()
		mu.Lock()
		intervals = append(intervals, [2]time.Time{start, end})
		mu.Unlock()
		return nil
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if _, err := h.SetPersisted(caA.CertPEM(), record); err != nil {
			t.Errorf("SetPersisted(caA): %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		if _, err := h.SetPersisted(caB.CertPEM(), record); err != nil {
			t.Errorf("SetPersisted(caB): %v", err)
		}
	}()
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if len(intervals) != 2 {
		t.Fatalf("expected 2 persist calls, got %d", len(intervals))
	}
	a, b := intervals[0], intervals[1]
	overlap := a[0].Before(b[1]) && b[0].Before(a[1])
	if overlap {
		t.Errorf("persist callbacks overlapped (%v..%v and %v..%v), want serialized writers", a[0], a[1], b[0], b[1])
	}
}
