package worker_test

import (
	"crypto/tls"
	"testing"

	"github.com/rvben/shinyhub/internal/worker"
)

func TestAgentServer_TLSConfig_RequiresClientCert(t *testing.T) {
	// Build a minimal CA and server cert to verify the TLS config shape.
	ca, err := worker.OpenCA(t.TempDir(), []string{"tok"})
	if err != nil {
		t.Fatalf("OpenCA: %v", err)
	}
	serverCert, err := ca.ServerCertificate("127.0.0.1")
	if err != nil {
		t.Fatalf("ServerCertificate: %v", err)
	}

	srv := worker.NewAgentServer(worker.AgentServerConfig{
		ListenAddr: "127.0.0.1:0",
		CertSource: worker.NewCertHolder(serverCert),
		ClientCAs:  ca.Pool(),
		NodeID:     "node-a",
	})

	tlsCfg := srv.TLSConfig()

	if tlsCfg.ClientAuth != tls.RequireAndVerifyClientCert {
		t.Errorf("ClientAuth = %v, want RequireAndVerifyClientCert", tlsCfg.ClientAuth)
	}
	if tlsCfg.ClientCAs == nil {
		t.Error("ClientCAs is nil")
	}
	if tlsCfg.MinVersion != tls.VersionTLS12 {
		t.Errorf("MinVersion = %v, want TLS 1.2", tlsCfg.MinVersion)
	}
	// HTTP/1.1 only so NDJSON streaming and WebSocket upgrades work end to end.
	if len(tlsCfg.NextProtos) != 1 || tlsCfg.NextProtos[0] != "http/1.1" {
		t.Errorf("NextProtos = %v, want [http/1.1]", tlsCfg.NextProtos)
	}
	// The server cert is served via GetCertificate (holder-backed) so a renewed
	// cert can be swapped in without restarting the listener.
	if tlsCfg.GetCertificate == nil {
		t.Fatal("GetCertificate is nil; server cert is not holder-backed")
	}
	got, err := tlsCfg.GetCertificate(&tls.ClientHelloInfo{})
	if err != nil {
		t.Fatalf("GetCertificate: %v", err)
	}
	if got == nil || len(got.Certificate) == 0 {
		t.Error("no server certificate configured")
	}
}
