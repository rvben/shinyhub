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
	"net"
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
	// CAPEM pins the control plane's CA certificate for the join handshake.
	// The worker has no CA bundle until register completes, so without this
	// the register TLS connection verifies against the host's system roots and
	// fails for the internal self-signed CA. When empty, system roots are used
	// (sufficient only when the worker API is fronted by a publicly trusted
	// certificate). After register, the CA bundle the control plane returns
	// pins every subsequent mTLS call.
	CAPEM []byte
}

// Agent is a running worker: it holds its identity (node id + signed cert), an
// mTLS client to the control plane, and the local process Manager.
type Agent struct {
	cfg     Config
	nodeID  string
	client  *worker.Client
	cache   *BundleCache
	cacerts *worker.CAHolder

	// certs holds the current issued keypair. The inbound mTLS server and the
	// outbound control-plane client both read it through the holder, so a
	// renewed cert applied during the heartbeat loop takes effect on both
	// without a restart.
	certs *worker.CertHolder
	// keyPEM and csrPEM are retained so the agent can renew its certificate: it
	// resubmits the same CSR on heartbeat and rebuilds the keypair from the
	// re-signed cert plus this key.
	keyPEM []byte
	csrPEM []byte

	// renewFailures counts consecutive heartbeats where renewal was due but not
	// fulfilled. It accompanies the escalating renewal log so an operator can see
	// how long renewal has been stuck; a successful renewal resets it to zero.
	renewFailures int

	// Listen binds the agent's inbound mTLS listener. Run calls it synchronously
	// before its up-front heartbeat, so a bind failure (e.g. the advertised port
	// is already in use) is returned before the worker ever announces itself up,
	// and a success means the port is already held when it does. Serve then runs
	// concurrently with the heartbeat loop on the bound listener. The agent's
	// inbound mTLS server sets both before Run is called; nil disables the
	// inbound server (used by tests that only exercise heartbeating).
	Listen func() (net.Listener, error)
	Serve  func(ctx context.Context, ln net.Listener) error
}

// NodeID returns the control-plane-assigned node id.
func (a *Agent) NodeID() string { return a.nodeID }

// Bundles returns the agent's bundle cache so the replica server can pull
// and mount app bundles by content digest on demand.
func (a *Agent) Bundles() *BundleCache { return a.cache }

// Certs returns the holder for the cert issued during Bootstrap. The inbound
// mTLS server reads its identity through it, so a cert renewed on heartbeat
// takes effect on the next handshake.
func (a *Agent) Certs() *worker.CertHolder { return a.certs }

// CACerts returns the holder for the CA bundle pinned during Bootstrap. The
// inbound server reads its ClientCAs through it, so a bundle rotated on
// heartbeat takes effect on the next handshake.
func (a *Agent) CACerts() *worker.CAHolder { return a.cacerts }

// Bootstrap returns a ready agent, reusing a still-valid persisted identity when
// one exists and otherwise performing the join handshake. Re-adopting an on-disk
// cert keeps the worker's node id stable across restarts and lets an established
// worker restart even after the operator has rotated away its join token.
func Bootstrap(ctx context.Context, cfg Config) (*Agent, error) {
	agentDir := filepath.Join(cfg.DataDir, "agent")
	if err := os.MkdirAll(agentDir, 0o700); err != nil {
		return nil, fmt.Errorf("agent dir: %w", err)
	}

	if ag, ok, err := readoptFromDisk(cfg, agentDir, time.Now()); err != nil {
		return nil, err
	} else if ok {
		return ag, nil
	}

	return register(ctx, cfg, agentDir)
}

// register performs the join handshake: it generates a fresh keypair and CSR,
// registers with the control plane, persists the issued identity, and builds the
// agent. Changing --advertise-addr or --tier on an already-joined worker requires
// clearing the agent data dir so this fresh-join path runs and carries the new
// values to the control plane (heartbeat does not).
func register(ctx context.Context, cfg Config, agentDir string) (*Agent, error) {
	// The join token is required only to register a new worker; re-adopting a
	// persisted identity (handled before this path) needs none. Enforcing it here
	// rather than at the CLI lets a worker restart after its join token has been
	// rotated away, as long as its on-disk certificate is still valid.
	if cfg.Token == "" {
		return nil, fmt.Errorf("join token required to register a new worker")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}
	csrPEM, err := csrPEMFromKey(key)
	if err != nil {
		return nil, err
	}

	resp, err := worker.Register(ctx, cfg.ServerURL, workerapi.RegisterRequest{
		Token:         cfg.Token,
		Name:          cfg.Name,
		AdvertiseAddr: cfg.AdvertiseAddr,
		Tier:          cfg.Tier,
		Version:       cfg.Version,
		CSRPEM:        string(csrPEM),
	}, cfg.CAPEM)
	if err != nil {
		return nil, fmt.Errorf("register: %w", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("marshal key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	// Persist identity so a restart re-adopts it without re-registering.
	if err := os.WriteFile(filepath.Join(agentDir, "client-key.pem"), keyPEM, 0o600); err != nil {
		return nil, fmt.Errorf("write key: %w", err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "client-cert.pem"), []byte(resp.CertPEM), 0o600); err != nil {
		return nil, fmt.Errorf("write cert: %w", err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "ca-bundle.pem"), []byte(resp.CABundle), 0o600); err != nil {
		return nil, fmt.Errorf("write ca bundle: %w", err)
	}

	return buildAgent(cfg, resp.NodeID, keyPEM, csrPEM, []byte(resp.CertPEM), []byte(resp.CABundle))
}

// readoptFromDisk rebuilds the agent from a previously persisted identity when
// one is present and its certificate is still valid as of now. A missing,
// unparseable, or expired identity returns ok=false so Bootstrap falls back to a
// fresh join; only an unrecoverable build error (after a usable identity was
// found) returns a non-nil error.
func readoptFromDisk(cfg Config, agentDir string, now time.Time) (*Agent, bool, error) {
	keyPEM, err := os.ReadFile(filepath.Join(agentDir, "client-key.pem"))
	if err != nil {
		return nil, false, nil
	}
	certPEM, err := os.ReadFile(filepath.Join(agentDir, "client-cert.pem"))
	if err != nil {
		return nil, false, nil
	}
	caBundle, err := os.ReadFile(filepath.Join(agentDir, "ca-bundle.pem"))
	if err != nil {
		return nil, false, nil
	}

	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil {
		return nil, false, nil
	}
	leaf, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, false, nil
	}
	if !now.Before(leaf.NotAfter) {
		return nil, false, nil // expired: re-join to get a fresh cert
	}
	nodeID := worker.NodeIDFromCert(leaf)
	if nodeID == "" {
		return nil, false, nil
	}

	// Rebuild a CSR from the persisted key so heartbeat renewal still works.
	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return nil, false, nil
	}
	key, err := x509.ParseECPrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, false, nil
	}
	csrPEM, err := csrPEMFromKey(key)
	if err != nil {
		return nil, false, err
	}

	ag, err := buildAgent(cfg, nodeID, keyPEM, csrPEM, certPEM, caBundle)
	if err != nil {
		return nil, false, err
	}
	slog.Info("worker re-adopted persisted identity", "node_id", nodeID, "cert_not_after", leaf.NotAfter)
	return ag, true, nil
}

// csrPEMFromKey builds the worker's PEM-encoded CSR from its private key. The
// same subject is used at join and renewal; the control plane binds the node id
// from the presented cert, not the CSR subject.
func csrPEMFromKey(key *ecdsa.PrivateKey) ([]byte, error) {
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: "shinyhub-worker"},
	}, key)
	if err != nil {
		return nil, fmt.Errorf("create csr: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER}), nil
}

// buildAgent assembles an Agent from an identity (issued cert + retained key +
// CSR) and the pinned CA bundle. Both the fresh-join and re-adopt paths converge
// here so the running agent is wired identically however it obtained its cert.
func buildAgent(cfg Config, nodeID string, keyPEM, csrPEM, certPEM, caBundle []byte) (*Agent, error) {
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("load issued keypair: %w", err)
	}
	cacerts, err := worker.NewCAHolder(caBundle)
	if err != nil {
		return nil, err
	}
	certs := worker.NewCertHolder(cert)
	client, err := worker.NewClient(cfg.ServerURL, certs, cacerts)
	if err != nil {
		return nil, fmt.Errorf("build mtls client: %w", err)
	}

	ag := &Agent{cfg: cfg, nodeID: nodeID, client: client, certs: certs, keyPEM: keyPEM, csrPEM: csrPEM, cacerts: cacerts}
	ag.cache = NewBundleCache(filepath.Join(cfg.DataDir, "bundles"), func(ctx context.Context, digest string) (io.ReadCloser, error) {
		return client.FetchBundle(ctx, digest)
	})
	return ag, nil
}

// heartbeatOnce posts a single heartbeat, requesting certificate renewal when
// the current cert is past its half-life, applying any renewed cert and rotated
// CA bundle the control plane returns, and logging the renewal outcome with a
// severity that escalates as an unfulfilled renewal nears the cert's expiry.
func (a *Agent) heartbeatOnce(ctx context.Context) error {
	now := time.Now()
	phase, notAfter := a.currentRenewalPhase(now)
	csr := ""
	if phase >= renewalDue {
		csr = string(a.csrPEM)
	}

	resp, err := a.client.Heartbeat(ctx, a.cfg.Version, csr)
	if err != nil {
		a.recordRenewal(ctx, phase, notAfter, false, err)
		return err
	}
	// Apply a rotated CA bundle before the renewed cert: the new cert may be
	// signed by the new CA, and the worker must trust that root first.
	if resp.CABundle != "" {
		if err := a.applyCABundle(resp.CABundle); err != nil {
			return err
		}
	}
	renewed := false
	if resp.CertPEM != "" {
		if err := a.applyRenewedCert(resp.CertPEM); err != nil {
			return err
		}
		renewed = true
		// Report the freshly issued cert's expiry, not the cert just replaced.
		_, notAfter = a.currentRenewalPhase(now)
	}
	a.recordRenewal(ctx, phase, notAfter, renewed, nil)
	return nil
}

// Run blocks, heartbeating every interval until ctx is cancelled. If Listen is
// set, the inbound listener is bound synchronously first (a bind failure aborts
// Run before any heartbeat) and then served concurrently; a non-nil error from
// Serve terminates the run loop.
func (a *Agent) Run(ctx context.Context, interval time.Duration) error {
	t := time.NewTicker(interval)
	defer t.Stop()

	serveErrCh := make(chan error, 1)
	listening := false
	if a.Listen != nil {
		// Bind before announcing liveness. Binding is the fail-fast half of
		// serving, so doing it synchronously means a port conflict surfaces here,
		// before the up-front heartbeat, and a success guarantees the port is held
		// when the worker reports up. This closes the race a deferred bind would
		// open: heartbeating up just as the serving side fails to start.
		ln, err := a.Listen()
		if err != nil {
			return fmt.Errorf("agent server: %w", err)
		}
		listening = true
		go func() { serveErrCh <- a.Serve(ctx, ln) }()
	}

	// announceReady emits the readiness signal exactly once, on the first
	// SUCCESSFUL heartbeat after the listener bound. At that point the worker is
	// both listening (Listen succeeded above) and routable (the control plane
	// promotes it joining->up on a heartbeat), so anything waiting on this signal
	// can safely route to it. Tying readiness to the first success rather than
	// specifically the up-front heartbeat means a transient initial failure that a
	// later tick recovers still announces readiness instead of hanging the waiter.
	ready := false
	announceReady := func(hbErr error) {
		if !ready && hbErr == nil && listening {
			ready = true
			slog.Info("worker data-plane ready", "advertise_addr", a.cfg.AdvertiseAddr)
		}
	}

	// Heartbeat once up front so a freshly bootstrapped or re-adopted worker
	// checks in (and renews a cert already past its half-life) without waiting a
	// full interval. This closes the window where a re-adopted cert with little
	// life left would expire before the first ticker-driven renewal.
	if ctx.Err() == nil {
		err := a.heartbeatOnce(ctx)
		if err != nil {
			slog.Warn("worker heartbeat failed", "err", err)
		}
		announceReady(err)
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
			err := a.heartbeatOnce(ctx)
			if err != nil {
				slog.Warn("worker heartbeat failed", "err", err)
			}
			announceReady(err)
		}
	}
}
