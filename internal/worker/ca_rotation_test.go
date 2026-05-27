package worker

import (
	"context"
	"crypto/tls"
	"net/http"
	"testing"
	"time"
)

// serveTLS starts an HTTP server on a TLS listener built from cfg and returns
// its address, shutting down when the test ends.
func serveTLS(t *testing.T, cfg *tls.Config, h http.Handler) string {
	t.Helper()
	ln, err := tls.Listen("tcp", "127.0.0.1:0", cfg)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &http.Server{Handler: h}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})
	return ln.Addr().String()
}

// TestClient_HotReloadsServerCATrust verifies that the worker's outbound client
// re-evaluates the control plane's server certificate against the live CA pool,
// so rotating the bundle into the holder lets a previously-untrusted control
// plane be reached without rebuilding the client.
func TestClient_HotReloadsServerCATrust(t *testing.T) {
	trusted, err := OpenCA(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("open trusted ca: %v", err)
	}
	rotated, err := OpenCA(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("open rotated ca: %v", err)
	}

	// The control plane presents a server cert signed by the rotated CA, which
	// the client does not trust yet.
	srvCert, err := rotated.ServerCertificate("127.0.0.1")
	if err != nil {
		t.Fatalf("server cert: %v", err)
	}
	addr := serveTLS(t, &tls.Config{
		Certificates: []tls.Certificate{srvCert},
		MinVersion:   tls.VersionTLS12,
	}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))

	caSource, err := NewCAHolder(trusted.CertPEM())
	if err != nil {
		t.Fatalf("ca holder: %v", err)
	}
	c, err := NewClient("https://"+addr, NewCertHolder(tls.Certificate{}), caSource)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := c.Heartbeat(ctx, "v", ""); err == nil {
		t.Fatal("expected TLS failure: control-plane cert signed by untrusted CA")
	}

	if _, err := caSource.Set(rotated.CertPEM()); err != nil {
		t.Fatalf("rotate CA trust: %v", err)
	}
	if _, err := c.Heartbeat(ctx, "v", ""); err != nil {
		t.Fatalf("after CA rotation the control plane must be reachable: %v", err)
	}
}

// TestAgentServer_HotReloadsClientCATrust verifies that the worker's inbound
// mTLS server re-evaluates the control plane's client certificate against the
// live CA pool, so rotating the bundle lets a control plane whose client cert is
// signed by a new CA authenticate without restarting the listener.
func TestAgentServer_HotReloadsClientCATrust(t *testing.T) {
	serverCA, err := OpenCA(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("open server ca: %v", err)
	}
	clientCAOld, err := OpenCA(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("open old client ca: %v", err)
	}
	clientCANew, err := OpenCA(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("open new client ca: %v", err)
	}

	serverCert, err := serverCA.ServerCertificate("127.0.0.1")
	if err != nil {
		t.Fatalf("server cert: %v", err)
	}
	caSource, err := NewCAHolder(clientCAOld.CertPEM())
	if err != nil {
		t.Fatalf("ca holder: %v", err)
	}
	srv := NewAgentServer(AgentServerConfig{
		CertSource: NewCertHolder(serverCert),
		CASource:   caSource,
		NodeID:     "node-srv",
	})
	addr := serveTLS(t, srv.TLSConfig(),
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))

	// The control plane presents a client cert signed by the new CA.
	cpCert := newWorkerClientCert(t, clientCANew, "node-cp")
	dial := func() error {
		c := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
			Certificates: []tls.Certificate{cpCert},
			RootCAs:      serverCA.Pool(),
			ServerName:   "127.0.0.1",
			MinVersion:   tls.VersionTLS12,
		}}}
		defer c.CloseIdleConnections()
		resp, err := c.Get("https://" + addr + "/")
		if err != nil {
			return err
		}
		_ = resp.Body.Close()
		return nil
	}

	if err := dial(); err == nil {
		t.Fatal("expected handshake rejection: client cert signed by untrusted CA")
	}
	if _, err := caSource.Set(clientCANew.CertPEM()); err != nil {
		t.Fatalf("rotate client CA trust: %v", err)
	}
	if err := dial(); err != nil {
		t.Fatalf("after rotating client-CA trust the handshake must succeed: %v", err)
	}
}
