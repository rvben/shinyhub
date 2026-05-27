package worker

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/rvben/shinyhub/internal/db"
)

// certPastHalfLife reports whether a cert spanning [notBefore, notAfter] is at
// or past its half-life as of now, the point at which the control plane re-mints
// its client cert so it never dials a worker with an expired identity.
func certPastHalfLife(notBefore, notAfter, now time.Time) bool {
	halfLife := notBefore.Add(notAfter.Sub(notBefore) / 2)
	return !now.Before(halfLife)
}

// rotatingClientCert lazily re-mints the control plane's client certificate once
// the current one passes its half-life, so long-running control planes never
// present an expired cert to worker agents. It is safe for concurrent use.
type rotatingClientCert struct {
	mint func() (tls.Certificate, error)

	mu        sync.Mutex
	cert      tls.Certificate
	notBefore time.Time
	notAfter  time.Time
}

func newRotatingClientCert(mint func() (tls.Certificate, error)) (*rotatingClientCert, error) {
	r := &rotatingClientCert{mint: mint}
	if err := r.refresh(); err != nil {
		return nil, err
	}
	return r, nil
}

// refresh mints a new cert and records its validity window. Caller holds mu.
func (r *rotatingClientCert) refresh() error {
	c, err := r.mint()
	if err != nil {
		return err
	}
	leaf := c.Leaf
	if leaf == nil {
		leaf, err = x509.ParseCertificate(c.Certificate[0])
		if err != nil {
			return fmt.Errorf("parse minted cert: %w", err)
		}
	}
	r.cert = c
	r.notBefore = leaf.NotBefore
	r.notAfter = leaf.NotAfter
	return nil
}

// getClientCertificate adapts the provider to tls.Config.GetClientCertificate,
// re-minting when the held cert is past its half-life. If a re-mint fails while
// the current cert is still valid, the current cert is used so a transient
// signing error does not break an otherwise-working dialer.
func (r *rotatingClientCert) getClientCertificate(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if certPastHalfLife(r.notBefore, r.notAfter, time.Now()) {
		if err := r.refresh(); err != nil {
			if time.Now().Before(r.notAfter) {
				return &r.cert, nil
			}
			return nil, err
		}
	}
	return &r.cert, nil
}

// mtlsDialer builds per-worker HTTP/1.1 mTLS clients for the control plane.
type mtlsDialer struct {
	clientCert *rotatingClientCert
	caPool     *x509.CertPool
}

// NewMTLSDialer constructs the default control-plane-to-worker dialer. mintClient
// issues the control plane's short-lived client certificate; the dialer re-mints
// it past its half-life so it is never presented expired. It returns the
// AgentDialer interface because callers wire it into a remoteRuntime, which
// depends only on the interface.
func NewMTLSDialer(mintClient func() (tls.Certificate, error), caPool *x509.CertPool) (AgentDialer, error) {
	rc, err := newRotatingClientCert(mintClient)
	if err != nil {
		return nil, err
	}
	return &mtlsDialer{clientCert: rc, caPool: caPool}, nil
}

func (d *mtlsDialer) tlsConfig(w db.Worker) *tls.Config {
	return &tls.Config{
		GetClientCertificate: d.clientCert.getClientCertificate,
		RootCAs:              d.caPool,
		// ServerName matches the DNS SAN in the worker's issued cert:
		// <nodeid>.node.shinyhub.internal
		ServerName: w.NodeID + nodeIDSANSuffix,
		NextProtos: []string{"http/1.1"},
		MinVersion: tls.VersionTLS12,
	}
}

func (d *mtlsDialer) transport(w db.Worker) *http.Transport {
	return &http.Transport{
		TLSClientConfig:   d.tlsConfig(w),
		ForceAttemptHTTP2: false,
	}
}

func (d *mtlsDialer) DialWorker(w db.Worker) (*http.Client, string, error) {
	if w.AdvertiseAddr == "" {
		return nil, "", fmt.Errorf("worker %q has no advertise address", w.NodeID)
	}
	client := &http.Client{Transport: d.transport(w)}
	return client, "https://" + w.AdvertiseAddr, nil
}

func (d *mtlsDialer) Transport(w db.Worker) (http.RoundTripper, error) {
	return d.transport(w), nil
}

var _ AgentDialer = (*mtlsDialer)(nil)
