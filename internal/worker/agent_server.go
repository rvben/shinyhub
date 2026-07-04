package worker

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
)

// AgentServerConfig configures the agent's inbound mTLS HTTP server.
type AgentServerConfig struct {
	// ListenAddr is the host:port the server binds. The agent command passes
	// the --advertise-addr value (or a port offset from it).
	ListenAddr string
	// CertSource holds the issued keypair from SignWorkerCSR (ServerAuth +
	// ClientAuth EKUs, <nodeid>.node.shinyhub.internal SAN). Reading the cert
	// through the holder lets the agent swap a renewed cert in without restarting
	// the listener, so the worker's routing surface survives cert rotation.
	CertSource *CertHolder
	// CASource holds the CA pool the worker trusts; the control plane must present
	// a client cert signed by this CA to authenticate. Reading it through the
	// holder lets a CA bundle rotated on heartbeat take effect on the next
	// handshake without restarting the listener.
	CASource *CAHolder
	// NodeID is the assigned node id, embedded in the server cert SAN.
	NodeID string
	// Replicas is the replica-control server whose Routes are mounted. Set
	// after construction once the replicaServer is built in worker.go.
	Replicas *replicaServer
}

// AgentServer is the agent's inbound mTLS HTTPS listener. The control plane
// dials it to issue Start/Signal/Wait/Stats/RunOnce commands and to proxy
// data-plane traffic through /v1/data/{token}/*.
type AgentServer struct {
	cfg AgentServerConfig
}

// NewAgentServer constructs an AgentServer from cfg.
func NewAgentServer(cfg AgentServerConfig) *AgentServer {
	return &AgentServer{cfg: cfg}
}

// TLSConfig returns the tls.Config for the listener. The served cert and the
// client-CA pool are resolved per handshake through GetConfigForClient so a
// renewed cert or a rotated CA bundle takes effect on the next connection
// without restarting the listener. Exposed so tests can verify the security
// posture without binding a port.
func (s *AgentServer) TLSConfig() *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
			return &tls.Config{
				GetCertificate: s.cfg.CertSource.GetCertificate,
				ClientAuth:     tls.RequireAndVerifyClientCert,
				ClientCAs:      s.cfg.CASource.Pool(),
				MinVersion:     tls.VersionTLS12,
				NextProtos:     []string{"http/1.1"},
				// Chain verification (RequireAndVerifyClientCert + ClientCAs) proves
				// the peer holds a cert signed by the shared worker CA, but every
				// worker's own dual-use cert satisfies that. Pin the peer identity to
				// the control plane so one compromised worker cannot dial another
				// worker's agent listener and launch arbitrary processes there.
				VerifyConnection: func(cs tls.ConnectionState) error {
					if len(cs.PeerCertificates) == 0 {
						return errors.New("worker agent: missing client certificate")
					}
					if !IsControlPlaneClientCert(cs.PeerCertificates[0]) {
						return errors.New("worker agent: client is not the control plane")
					}
					return nil
				},
			}, nil
		},
	}
}

// Listen binds an mTLS listener on the configured ListenAddr. It is the
// fail-fast half of serving: agent.Run binds synchronously so a port conflict
// surfaces before the worker announces liveness, then hands the listener to
// ServeListener to serve until ctx is cancelled.
func (s *AgentServer) Listen() (net.Listener, error) {
	return tls.Listen("tcp", s.cfg.ListenAddr, s.TLSConfig())
}

// ServeListener serves the replica-control API and data-plane proxy on ln until
// ctx is cancelled. ln must already terminate TLS (e.g. from tls.Listen with
// TLSConfig); callers that need a known bound port can pass their own listener.
func (s *AgentServer) ServeListener(ctx context.Context, ln net.Listener) error {
	r := chi.NewRouter()
	if s.cfg.Replicas != nil {
		s.cfg.Replicas.Routes(r)
	}
	srv := &http.Server{Handler: r}
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
