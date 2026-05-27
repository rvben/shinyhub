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
type CAHolder struct {
	mu   sync.RWMutex
	pool *x509.CertPool
	pem  []byte
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
// trust roots.
func (h *CAHolder) Set(caPEM []byte) (bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if bytes.Equal(caPEM, h.pem) {
		return false, nil
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return false, fmt.Errorf("parse CA bundle")
	}
	h.pool = pool
	h.pem = append([]byte(nil), caPEM...)
	return true, nil
}
