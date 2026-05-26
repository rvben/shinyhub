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

// NewClient builds an mTLS client from the signed cert keypair and pinned CA.
func NewClient(serverURL string, cert tls.Certificate, caPEM []byte) (*Client, error) {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("invalid CA bundle")
	}
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{
			MinVersion:   tls.VersionTLS12,
			Certificates: []tls.Certificate{cert},
			RootCAs:      pool,
		},
		ForceAttemptHTTP2: true,
	}
	return &Client{serverURL: serverURL, httpc: &http.Client{Transport: tr}}, nil
}

// Transport exposes the underlying transport for the data-plane tunnel (C1).
func (c *Client) Transport() http.RoundTripper { return c.httpc.Transport }

// Heartbeat posts a heartbeat and applies any renewed cert the CP returns.
func (c *Client) Heartbeat(ctx context.Context, version string) (workerapi.HeartbeatResponse, error) {
	body, _ := json.Marshal(workerapi.HeartbeatRequest{Version: version})
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
