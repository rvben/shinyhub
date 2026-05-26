package worker

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
)

// AgentServerConfig configures the agent's inbound mTLS HTTP server.
type AgentServerConfig struct {
	// ListenAddr is the host:port the server binds. The agent command passes
	// the --advertise-addr value (or a port offset from it).
	ListenAddr string
	// ServerCert is the issued keypair from SignWorkerCSR. It carries both
	// ServerAuth and ClientAuth EKUs and the <nodeid>.node.shinyhub.internal SAN.
	ServerCert tls.Certificate
	// ClientCAs is the CA pool workers trust; the control plane must present a
	// cert signed by this CA to authenticate.
	ClientCAs *x509.CertPool
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

// TLSConfig returns the tls.Config for the listener. Exposed so tests can
// verify the security posture without binding a port.
func (s *AgentServer) TLSConfig() *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{s.cfg.ServerCert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    s.cfg.ClientCAs,
		MinVersion:   tls.VersionTLS12,
		NextProtos:   []string{"http/1.1"},
	}
}

// Serve starts the HTTPS server and blocks until ctx is cancelled. It runs the
// replica-control API and data-plane proxy on the chi router. Intended to be
// called in a goroutine alongside the heartbeat loop in agent.Run.
func (s *AgentServer) Serve(ctx context.Context) error {
	r := chi.NewRouter()
	if s.cfg.Replicas != nil {
		s.cfg.Replicas.Routes(r)
	}
	srv := &http.Server{
		Addr:      s.cfg.ListenAddr,
		Handler:   r,
		TLSConfig: s.TLSConfig(),
	}
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()
	if err := srv.ListenAndServeTLS("", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
