// internal/worker/agent_server_peer_test.go
package worker

import (
	"crypto/tls"
	"crypto/x509"
	"net"
	"testing"
	"time"
)

// agentHandshake performs a full mTLS handshake against the agent server's TLS
// config using clientCert, returning the handshake error (nil on success).
func agentHandshake(t *testing.T, serverCfg *tls.Config, clientCert tls.Certificate, caPEM []byte) error {
	t.Helper()
	clientPool := x509.NewCertPool()
	if !clientPool.AppendCertsFromPEM(caPEM) {
		t.Fatal("append CA PEM")
	}
	clientConnRaw, serverConnRaw := net.Pipe()
	deadline := time.Now().Add(5 * time.Second)
	_ = clientConnRaw.SetDeadline(deadline)
	_ = serverConnRaw.SetDeadline(deadline)
	defer clientConnRaw.Close()
	defer serverConnRaw.Close()

	serverConn := tls.Server(serverConnRaw, serverCfg)
	clientConn := tls.Client(clientConnRaw, &tls.Config{
		Certificates: []tls.Certificate{clientCert},
		RootCAs:      clientPool,
		ServerName:   "127.0.0.1",
		MinVersion:   tls.VersionTLS12,
	})

	srvErr := make(chan error, 1)
	go func() { srvErr <- serverConn.Handshake() }()
	clientErr := clientConn.Handshake()
	serverErr := <-srvErr
	if clientErr != nil {
		return clientErr
	}
	return serverErr
}

// TestAgentServer_TLSConfig_PinsControlPlaneIdentity proves the worker agent
// listener accepts ONLY the control plane's client certificate and rejects any
// other worker's own CA-signed certificate. Without peer-identity pinning any
// worker could dial another worker's agent listener and launch processes there
// (fleet-wide RCE, SEC-C1).
func TestAgentServer_TLSConfig_PinsControlPlaneIdentity(t *testing.T) {
	ca, err := OpenCA(t.TempDir(), []string{"tok"})
	if err != nil {
		t.Fatalf("OpenCA: %v", err)
	}
	serverCert, err := ca.ServerCertificate("127.0.0.1")
	if err != nil {
		t.Fatalf("ServerCertificate: %v", err)
	}
	caSource, err := NewCAHolder(ca.CertPEM())
	if err != nil {
		t.Fatalf("NewCAHolder: %v", err)
	}
	srv := NewAgentServer(AgentServerConfig{
		ListenAddr: "127.0.0.1:0",
		CertSource: NewCertHolder(serverCert),
		CASource:   caSource,
		NodeID:     "node-a",
	})
	serverCfg, err := srv.TLSConfig().GetConfigForClient(&tls.ClientHelloInfo{})
	if err != nil {
		t.Fatalf("GetConfigForClient: %v", err)
	}

	// The control plane's own client cert must be accepted.
	cpCert, err := ca.ControlClientCertificate()
	if err != nil {
		t.Fatalf("ControlClientCertificate: %v", err)
	}
	if err := agentHandshake(t, serverCfg.Clone(), cpCert, ca.CertPEM()); err != nil {
		t.Fatalf("control-plane client cert rejected, want accepted: %v", err)
	}

	// A different worker's own valid CA-signed cert must be REJECTED.
	workerCert := newWorkerClientCert(t, ca, "node-b")
	if err := agentHandshake(t, serverCfg.Clone(), workerCert, ca.CertPEM()); err == nil {
		t.Fatal("worker client cert accepted by agent listener; expected rejection (SEC-C1 impersonation)")
	}
}
