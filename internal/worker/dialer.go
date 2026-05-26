package worker

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"

	"github.com/rvben/shinyhub/internal/db"
)

// mtlsDialer builds per-worker HTTP/1.1 mTLS clients for the control plane.
type mtlsDialer struct {
	clientCert tls.Certificate
	caPool     *x509.CertPool
}

func newMTLSDialer(clientCert tls.Certificate, caPool *x509.CertPool) *mtlsDialer {
	return &mtlsDialer{clientCert: clientCert, caPool: caPool}
}

// NewMTLSDialer constructs the default control-plane-to-worker dialer. It
// returns the AgentDialer interface because callers wire it into a
// remoteRuntime, which depends only on the interface.
func NewMTLSDialer(clientCert tls.Certificate, caPool *x509.CertPool) AgentDialer {
	return newMTLSDialer(clientCert, caPool)
}

func (d *mtlsDialer) tlsConfig(w db.Worker) *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{d.clientCert},
		RootCAs:      d.caPool,
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
