// internal/worker/agent/agent.go
package agent

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/rvben/shinyhub/internal/worker"
	workerapi "github.com/rvben/shinyhub/internal/worker/api"
)

// Config holds the worker agent's CLI-supplied settings.
type Config struct {
	ServerURL     string
	Token         string
	AdvertiseAddr string
	Tier          string
	DataDir       string
	Version       string
	Name          string
}

// Agent is a running worker: it holds its identity (node id + signed cert), an
// mTLS client to the control plane, and the local process Manager.
type Agent struct {
	cfg        Config
	nodeID     string
	client     *worker.Client
	cache      *BundleCache
	issuedCert tls.Certificate
	caPool     *x509.CertPool

	// ServeFunc, when non-nil, is run concurrently with the heartbeat loop.
	// The agent's inbound mTLS server sets this before Run is called.
	ServeFunc func(ctx context.Context) error
}

// NodeID returns the control-plane-assigned node id.
func (a *Agent) NodeID() string { return a.nodeID }

// Bundles returns the agent's bundle cache so the replica server can pull
// and mount app bundles by content digest on demand.
func (a *Agent) Bundles() *BundleCache { return a.cache }

// IssuedCert returns the cert issued by the control plane during Bootstrap.
// Used by the inbound mTLS server to present its identity.
func (a *Agent) IssuedCert() tls.Certificate { return a.issuedCert }

// CAPool returns the CA pool pinned during Bootstrap, used as the inbound
// server's ClientCAs to verify the control plane's client cert.
func (a *Agent) CAPool() *x509.CertPool { return a.caPool }

// Bootstrap performs the join handshake and returns a ready agent.
func Bootstrap(ctx context.Context, cfg Config) (*Agent, error) {
	agentDir := filepath.Join(cfg.DataDir, "agent")
	if err := os.MkdirAll(agentDir, 0o700); err != nil {
		return nil, fmt.Errorf("agent dir: %w", err)
	}

	// Generate the worker keypair and CSR.
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: "shinyhub-worker"},
	}, key)
	if err != nil {
		return nil, fmt.Errorf("create csr: %w", err)
	}
	csrPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})

	resp, err := worker.Register(ctx, cfg.ServerURL, workerapi.RegisterRequest{
		Token:         cfg.Token,
		Name:          cfg.Name,
		AdvertiseAddr: cfg.AdvertiseAddr,
		Tier:          cfg.Tier,
		Version:       cfg.Version,
		CSRPEM:        string(csrPEM),
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("register: %w", err)
	}

	// Persist identity for restart re-adoption.
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("marshal key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(filepath.Join(agentDir, "client-key.pem"), keyPEM, 0o600); err != nil {
		return nil, fmt.Errorf("write key: %w", err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "client-cert.pem"), []byte(resp.CertPEM), 0o600); err != nil {
		return nil, fmt.Errorf("write cert: %w", err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "ca-bundle.pem"), []byte(resp.CABundle), 0o600); err != nil {
		return nil, fmt.Errorf("write ca bundle: %w", err)
	}

	cert, err := tls.X509KeyPair([]byte(resp.CertPEM), keyPEM)
	if err != nil {
		return nil, fmt.Errorf("load issued keypair: %w", err)
	}
	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM([]byte(resp.CABundle)) {
		return nil, fmt.Errorf("parse CA bundle")
	}
	client, err := worker.NewClient(cfg.ServerURL, cert, []byte(resp.CABundle))
	if err != nil {
		return nil, fmt.Errorf("build mtls client: %w", err)
	}

	ag := &Agent{cfg: cfg, nodeID: resp.NodeID, client: client, issuedCert: cert, caPool: caPool}
	ag.cache = NewBundleCache(filepath.Join(cfg.DataDir, "bundles"), func(ctx context.Context, digest string) (io.ReadCloser, error) {
		return client.FetchBundle(ctx, digest)
	})
	return ag, nil
}

// heartbeatOnce posts a single heartbeat to the control plane.
func (a *Agent) heartbeatOnce(ctx context.Context) error {
	_, err := a.client.Heartbeat(ctx, a.cfg.Version)
	return err
}

// Run blocks, heartbeating every interval until ctx is cancelled. If
// ServeFunc is set, it is run concurrently; a non-nil error from it
// terminates the run loop.
func (a *Agent) Run(ctx context.Context, interval time.Duration) error {
	t := time.NewTicker(interval)
	defer t.Stop()

	serveErrCh := make(chan error, 1)
	if a.ServeFunc != nil {
		go func() { serveErrCh <- a.ServeFunc(ctx) }()
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-serveErrCh:
			if err != nil {
				return fmt.Errorf("agent server: %w", err)
			}
		case <-t.C:
			if err := a.heartbeatOnce(ctx); err != nil {
				slog.Warn("worker heartbeat failed", "err", err)
			}
		}
	}
}
