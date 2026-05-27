// internal/worker/client.go
package worker

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	workerapi "github.com/rvben/shinyhub/internal/worker/api"
)

// Client is a worker's mTLS HTTP client to its control plane. After bootstrap it
// carries the signed client cert and pins the CA; the same transport backs the
// data-plane tunnel the control-plane proxy dials in reverse (C1).
type Client struct {
	serverURL string
	httpc     *http.Client
}

// Register performs the join: POST the token + CSR over HTTPS pinned to caPEM
// (no client cert yet at join time). Returns the signed cert, CA bundle, and node id.
func Register(ctx context.Context, serverURL string, req workerapi.RegisterRequest, caPEM []byte) (workerapi.RegisterResponse, error) {
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if len(caPEM) > 0 {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caPEM) {
			return workerapi.RegisterResponse{}, fmt.Errorf("invalid pinned CA pem")
		}
		tlsCfg.RootCAs = pool
	}
	httpc := &http.Client{
		Timeout:   30 * time.Second,
		Transport: &http.Transport{TLSClientConfig: tlsCfg},
	}
	body, _ := json.Marshal(req)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, serverURL+"/api/workers/register", bytes.NewReader(body))
	if err != nil {
		return workerapi.RegisterResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := httpc.Do(httpReq)
	if err != nil {
		return workerapi.RegisterResponse{}, fmt.Errorf("register request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return workerapi.RegisterResponse{}, fmt.Errorf("register: %s: %s", resp.Status, msg)
	}
	var out workerapi.RegisterResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return workerapi.RegisterResponse{}, fmt.Errorf("decode register response: %w", err)
	}
	return out, nil
}

// NewClient builds an mTLS client that presents the certificate held by
// certSource and verifies the control plane against the trust bundle held by
// caSource. Sourcing both through holders lets the worker rotate its expiring
// cert and a rotated CA bundle without rebuilding the client.
func NewClient(serverURL string, certSource *CertHolder, caSource *CAHolder) (*Client, error) {
	host, err := hostFromURL(serverURL)
	if err != nil {
		return nil, err
	}
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{
			MinVersion:           tls.VersionTLS12,
			GetClientCertificate: certSource.GetClientCertificate,
			// Verification is done in VerifyConnection against the live CA pool.
			// Default verification snapshots RootCAs when the config is built and
			// cannot see a bundle rotated at runtime, so it is disabled here and
			// fully replaced (chain + host) below.
			InsecureSkipVerify: true,
			VerifyConnection:   verifyServerAgainst(caSource, host),
		},
		ForceAttemptHTTP2: true,
	}
	return &Client{serverURL: serverURL, httpc: &http.Client{Transport: tr}}, nil
}

// hostFromURL extracts the bare host (no port) the client verifies the control
// plane's certificate against.
func hostFromURL(serverURL string) (string, error) {
	u, err := url.Parse(serverURL)
	if err != nil {
		return "", fmt.Errorf("parse server url: %w", err)
	}
	if u.Host == "" {
		return "", fmt.Errorf("server url %q has no host", serverURL)
	}
	return u.Hostname(), nil
}

// verifyServerAgainst authenticates the control plane's server certificate
// against the current CA pool and expected host. Reading caSource.Pool() on
// every handshake is what makes a rotated CA bundle take effect without
// rebuilding the client. Because it runs on session resumption too, a rotated
// bundle is enforced even on resumed connections.
func verifyServerAgainst(caSource *CAHolder, host string) func(tls.ConnectionState) error {
	return func(cs tls.ConnectionState) error {
		if len(cs.PeerCertificates) == 0 {
			return fmt.Errorf("control plane presented no certificate")
		}
		inter := x509.NewCertPool()
		for _, c := range cs.PeerCertificates[1:] {
			inter.AddCert(c)
		}
		_, err := cs.PeerCertificates[0].Verify(x509.VerifyOptions{
			Roots:         caSource.Pool(),
			Intermediates: inter,
			DNSName:       host,
			KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		})
		return err
	}
}

// Transport exposes the underlying transport for the data-plane tunnel (C1).
func (c *Client) Transport() http.RoundTripper { return c.httpc.Transport }

// Heartbeat posts a heartbeat and returns the control plane's response. When
// renewCSRPEM is non-empty it is sent as a certificate renewal request; the
// caller applies any cert the response carries.
func (c *Client) Heartbeat(ctx context.Context, version, renewCSRPEM string) (workerapi.HeartbeatResponse, error) {
	body, _ := json.Marshal(workerapi.HeartbeatRequest{Version: version, RenewCSRPEM: renewCSRPEM})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.serverURL+"/api/workers/heartbeat", bytes.NewReader(body))
	if err != nil {
		return workerapi.HeartbeatResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpc.Do(req)
	if err != nil {
		return workerapi.HeartbeatResponse{}, fmt.Errorf("heartbeat: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return workerapi.HeartbeatResponse{}, fmt.Errorf("heartbeat: %s", resp.Status)
	}
	var out workerapi.HeartbeatResponse
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return out, nil
}

// FetchBundle streams the bundle zip for a content digest. The caller verifies
// the digest on the returned stream.
func (c *Client) FetchBundle(ctx context.Context, digest string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.serverURL+"/internal/bundles/"+digest, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch bundle: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("fetch bundle %s: %s", digest, resp.Status)
	}
	return resp.Body, nil
}
