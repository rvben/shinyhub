// internal/worker/agent/renewal.go
package agent

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// shouldRenew reports whether a certificate spanning [notBefore, notAfter] is at
// or past its half-life as of now, the point at which the agent proactively
// renews so a new cert is in hand well before the current one expires.
func shouldRenew(notBefore, notAfter, now time.Time) bool {
	halfLife := notBefore.Add(notAfter.Sub(notBefore) / 2)
	return !now.Before(halfLife)
}

// renewCSRIfDue returns the agent's CSR when the current certificate is past its
// half-life, signalling the control plane to re-sign it; otherwise it returns an
// empty string so the heartbeat carries no renewal request.
func (a *Agent) renewCSRIfDue(now time.Time) string {
	cur := a.certs.Get()
	if len(cur.Certificate) == 0 {
		return ""
	}
	leaf, err := x509.ParseCertificate(cur.Certificate[0])
	if err != nil {
		return ""
	}
	if shouldRenew(leaf.NotBefore, leaf.NotAfter, now) {
		return string(a.csrPEM)
	}
	return ""
}

// applyRenewedCert rebuilds the keypair from the re-signed cert and the retained
// private key, swaps it into the holder so live listeners and clients present it
// on their next handshake, and persists it for restart re-adoption.
func (a *Agent) applyRenewedCert(certPEM string) error {
	newCert, err := tls.X509KeyPair([]byte(certPEM), a.keyPEM)
	if err != nil {
		return fmt.Errorf("load renewed keypair: %w", err)
	}
	a.certs.Set(newCert)
	path := filepath.Join(a.cfg.DataDir, "agent", "client-cert.pem")
	if err := os.WriteFile(path, []byte(certPEM), 0o600); err != nil {
		return fmt.Errorf("persist renewed cert: %w", err)
	}
	return nil
}
