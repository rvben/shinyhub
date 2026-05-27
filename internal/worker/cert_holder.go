// internal/worker/cert_holder.go
package worker

import (
	"crypto/tls"
	"sync"
)

// CertHolder stores a TLS certificate that can be atomically swapped at runtime.
// A renewed certificate set via Set takes effect on the next TLS handshake of
// every listener and client wired to the holder through GetCertificate or
// GetClientCertificate, so a worker can rotate its expiring cert without
// restarting its inbound server or rebuilding its outbound client. The zero
// value is not usable; construct with NewCertHolder.
type CertHolder struct {
	mu   sync.RWMutex
	cert tls.Certificate
}

// NewCertHolder returns a holder seeded with cert.
func NewCertHolder(cert tls.Certificate) *CertHolder {
	return &CertHolder{cert: cert}
}

// Set replaces the held certificate. Subsequent handshakes present it.
func (h *CertHolder) Set(cert tls.Certificate) {
	h.mu.Lock()
	h.cert = cert
	h.mu.Unlock()
}

// Get returns the currently held certificate.
func (h *CertHolder) Get() tls.Certificate {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.cert
}

// GetCertificate adapts the holder to tls.Config.GetCertificate so a server
// presents the current certificate on each handshake.
func (h *CertHolder) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	c := h.Get()
	return &c, nil
}

// GetClientCertificate adapts the holder to tls.Config.GetClientCertificate so a
// client presents the current certificate on each handshake.
func (h *CertHolder) GetClientCertificate(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
	c := h.Get()
	return &c, nil
}
