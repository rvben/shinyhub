package worker

import (
	"bytes"
	"crypto/x509"
	"fmt"
	"sync"
)

// CAHolder holds the CA trust bundle a worker pins, behind a read-write lock so
// the inbound server and outbound client can read the current pool on every
// handshake while a heartbeat swaps in a rotated bundle without a restart. It is
// safe for concurrent use.
//
// mu guards only the in-memory pool/pem pointers and is held briefly, never
// across disk I/O: Pool() is read on every TLS handshake, so a slow persist
// call must never block it. writeMu is a separate lock held for the duration
// of SetPersisted's validate-then-persist sequence, serializing concurrent
// writers against each other (and thus against the on-disk file) without
// contending with readers.
type CAHolder struct {
	mu      sync.RWMutex
	writeMu sync.Mutex
	pool    *x509.CertPool
	pem     []byte
}

// NewCAHolder parses caPEM into a trust pool, erroring if no certificate parses.
func NewCAHolder(caPEM []byte) (*CAHolder, error) {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("parse CA bundle")
	}
	return &CAHolder{pool: pool, pem: append([]byte(nil), caPEM...)}, nil
}

// Pool returns the current trust pool. Callers must treat it as read-only.
func (h *CAHolder) Pool() *x509.CertPool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.pool
}

// Set replaces the bundle when caPEM differs from the current one, reporting
// whether a change was applied. An unparseable bundle is rejected and leaves the
// current pool intact, so a malformed rotation never strands the worker without
// trust roots. It is a thin wrapper around SetPersisted with no persist step.
func (h *CAHolder) Set(caPEM []byte) (bool, error) {
	return h.SetPersisted(caPEM, nil)
}

// SetPersisted validates caPEM, durably persists it, and only then swaps it
// into the holder, reporting whether a change was applied. persist, when
// non-nil, is called with the new bundle so the caller can save it to disk;
// SetPersisted calls it with none of the holder's locks held, since Pool() is
// read on every TLS handshake and must never block behind a slow disk write.
//
// Persisting before swapping matters: a bundle is trusted in memory only once
// it has been written to disk, so a failed persist leaves the holder and the
// caller's on-disk copy exactly as they were, and the caller's next attempt
// still sees a change to apply. A malformed bundle is rejected before persist
// ever runs, so it never reaches disk.
//
// writeMu serializes concurrent callers for the full validate-then-persist
// sequence, so two overlapping rotations never race each other's disk writes;
// it is distinct from mu, which guards only the brief in-memory pointer swap.
func (h *CAHolder) SetPersisted(caPEM []byte, persist func([]byte) error) (bool, error) {
	h.writeMu.Lock()
	defer h.writeMu.Unlock()

	h.mu.RLock()
	unchanged := bytes.Equal(caPEM, h.pem)
	h.mu.RUnlock()
	if unchanged {
		return false, nil
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return false, fmt.Errorf("parse CA bundle")
	}

	if persist != nil {
		if err := persist(caPEM); err != nil {
			return false, err
		}
	}

	pem := append([]byte(nil), caPEM...)
	h.mu.Lock()
	h.pool = pool
	h.pem = pem
	h.mu.Unlock()

	return true, nil
}
