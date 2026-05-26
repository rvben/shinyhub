package worker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"syscall"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/worker/api"
)

// ProviderRemoteDocker labels replicas started on a remote Docker worker.
const ProviderRemoteDocker = "remote_docker"

// WorkerLookup resolves the live worker for a tier. Implemented by *Registry
// in production; a stub is used in tests.
type WorkerLookup interface {
	WorkerForTier(tier string) (db.Worker, bool)
}

// AgentDialer returns an HTTP client and base URL for talking to a worker over
// its mTLS tunnel. It is a seam: production builds an mTLS client keyed by the
// worker's node id; tests supply a stub backed by httptest.
type AgentDialer interface {
	// DialWorker returns a client whose transport authenticates to the worker
	// and the base URL (scheme://host) to prefix request paths with.
	DialWorker(w db.Worker) (*http.Client, string, error)
	// Transport returns the RoundTripper used to reach the given worker's data
	// plane (for the proxy and health checks).
	Transport(w db.Worker) (http.RoundTripper, error)
}

// remoteRuntime implements process.Runtime by delegating to a worker agent
// resolved from the registry per call. It is bound to a tier, not a fixed
// worker, so replicas follow whichever worker is currently live for the tier.
type remoteRuntime struct {
	lookup WorkerLookup
	tier   string
	dialer AgentDialer
}

func newRemoteRuntime(lookup WorkerLookup, tier string, dialer AgentDialer) *remoteRuntime {
	return &remoteRuntime{lookup: lookup, tier: tier, dialer: dialer}
}

// NewRemoteRuntime builds a tier-bound runtime that delegates to whichever
// worker is currently live for the tier, dialing it over the mTLS tunnel.
func NewRemoteRuntime(lookup WorkerLookup, tier string, dialer AgentDialer) process.Runtime {
	return newRemoteRuntime(lookup, tier, dialer)
}

func encodeRemoteHandle(nodeID, containerID string) string {
	return nodeID + "/" + containerID
}

func decodeRemoteHandle(h string) (nodeID, containerID string, err error) {
	parts := strings.SplitN(h, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("malformed remote handle %q", h)
	}
	return parts[0], parts[1], nil
}

// liveWorker resolves the current worker for this runtime's tier, failing
// closed when none is live.
func (r *remoteRuntime) liveWorker() (db.Worker, error) {
	w, ok := r.lookup.WorkerForTier(r.tier)
	if !ok {
		return db.Worker{}, fmt.Errorf("no live worker for tier %q", r.tier)
	}
	return w, nil
}

// workerForHandle resolves the worker that owns an opaque handle, validating
// that the handle's node id matches the tier's currently-live worker.
func (r *remoteRuntime) workerForHandle(h process.RunHandle) (db.Worker, string, error) {
	nodeID, containerID, err := decodeRemoteHandle(h.ContainerID)
	if err != nil {
		return db.Worker{}, "", err
	}
	w, err := r.liveWorker()
	if err != nil {
		return db.Worker{}, "", err
	}
	if w.NodeID != nodeID {
		return db.Worker{}, "", fmt.Errorf("handle node %q does not match live worker %q", nodeID, w.NodeID)
	}
	return w, containerID, nil
}

func (r *remoteRuntime) HostPreparesDeps() bool    { return false }
func (r *remoteRuntime) AppBindHost() string       { return "0.0.0.0" }
func (r *remoteRuntime) HostProvidesAppData() bool { return false }

// ReplicaTransport returns the mTLS transport for the tier's live worker so the
// proxy and health paths can reach the data plane. Returns nil when no worker
// is live (callers fall back to the default transport, which will then fail the
// health check cleanly).
func (r *remoteRuntime) ReplicaTransport() http.RoundTripper {
	w, err := r.liveWorker()
	if err != nil {
		return nil
	}
	tr, err := r.dialer.Transport(w)
	if err != nil {
		return nil
	}
	return tr
}

func toStartRequest(p process.StartParams) api.ReplicaStartRequest {
	slugs := make([]string, 0, len(p.SharedMounts))
	for _, m := range p.SharedMounts {
		slugs = append(slugs, m.SourceSlug)
	}
	// Convert KEY=VALUE env slice to the map the wire type uses.
	envMap := make(map[string]string, len(p.Env))
	for _, kv := range p.Env {
		if idx := strings.Index(kv, "="); idx > 0 {
			envMap[kv[:idx]] = kv[idx+1:]
		}
	}
	return api.ReplicaStartRequest{
		Slug:             p.Slug,
		Index:            p.Index,
		Tier:             p.Tier,
		ContentDigest:    p.ContentDigest,
		AppVersion:       p.AppVersion,
		DeploymentID:     p.DeploymentID,
		Command:          p.Command,
		Env:              envMap,
		BindPort:         p.Port,
		SharedMountSlugs: slugs,
		MemoryLimitMB:    p.MemoryLimitMB,
		CPUQuotaPercent:  p.CPUQuotaPercent,
	}
}

// streamFrames reads NDJSON frames from rc, writing log data to logWriter,
// returning the first FrameResult data bytes, or an error from a FrameError frame.
// On a result, it spawns a goroutine to drain remaining log frames until close.
// Frame.Data carries raw bytes for FrameLog and JSON-marshalled payloads for
// FrameResult. Frame.Error carries the error string for FrameError.
func streamFrames(rc io.ReadCloser, logWriter io.Writer) (json.RawMessage, error) {
	dec := json.NewDecoder(bufio.NewReader(rc))
	for {
		var fr api.Frame
		if err := dec.Decode(&fr); err != nil {
			rc.Close()
			if err == io.EOF {
				return nil, fmt.Errorf("stream ended before result")
			}
			return nil, err
		}
		switch fr.Kind {
		case api.FrameLog:
			if logWriter != nil && len(fr.Data) > 0 {
				_, _ = logWriter.Write(fr.Data)
			}
		case api.FrameError:
			rc.Close()
			msg := fr.Error
			if msg == "" {
				msg = "unknown worker error"
			}
			return nil, fmt.Errorf("worker error: %s", msg)
		case api.FrameResult:
			// Drain remaining log frames in the background so the worker's
			// streaming write side does not block.
			go func() {
				defer rc.Close()
				for {
					var f api.Frame
					if dec.Decode(&f) != nil {
						return
					}
					if f.Kind == api.FrameLog && logWriter != nil && len(f.Data) > 0 {
						_, _ = logWriter.Write(f.Data)
					}
				}
			}()
			return fr.Data, nil
		}
	}
}

func (r *remoteRuntime) Start(ctx context.Context, p process.StartParams, logWriter io.Writer) (process.ReplicaEndpoint, error) {
	w, err := r.liveWorker()
	if err != nil {
		return process.ReplicaEndpoint{}, err
	}
	client, base, err := r.dialer.DialWorker(w)
	if err != nil {
		return process.ReplicaEndpoint{}, err
	}
	body, _ := json.Marshal(toStartRequest(p))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/replicas", bytes.NewReader(body))
	if err != nil {
		return process.ReplicaEndpoint{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return process.ReplicaEndpoint{}, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return process.ReplicaEndpoint{}, fmt.Errorf("worker start returned %d", resp.StatusCode)
	}
	payload, err := streamFrames(resp.Body, logWriter)
	if err != nil {
		return process.ReplicaEndpoint{}, err
	}
	var result api.ReplicaResult
	if err := json.Unmarshal(payload, &result); err != nil {
		return process.ReplicaEndpoint{}, err
	}
	return process.ReplicaEndpoint{
		URL:      result.URL,
		Provider: ProviderRemoteDocker,
		WorkerID: result.NodeID,
		Handle:   process.RunHandle{ContainerID: encodeRemoteHandle(result.NodeID, result.ContainerID)},
	}, nil
}

func (r *remoteRuntime) Signal(h process.RunHandle, sig syscall.Signal) error {
	w, containerID, err := r.workerForHandle(h)
	if err != nil {
		return err
	}
	client, base, err := r.dialer.DialWorker(w)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(api.SignalRequest{Signal: int(sig)})
	req, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/v1/replicas/%s/signal", base, containerID), bytes.NewReader(body))
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("worker signal returned %d", resp.StatusCode)
	}
	return nil
}

// Wait blocks until the remote replica exits. It returns only an error; the
// Manager calls Wait purely to detect exit, so exit codes are not surfaced here.
func (r *remoteRuntime) Wait(ctx context.Context, h process.RunHandle) error {
	w, containerID, err := r.workerForHandle(h)
	if err != nil {
		return err
	}
	client, base, err := r.dialer.DialWorker(w)
	if err != nil {
		return err
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/v1/replicas/%s/wait", base, containerID), nil)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("worker wait returned %d", resp.StatusCode)
	}
	return nil
}

func (r *remoteRuntime) Stats(ctx context.Context, h process.RunHandle) (float64, uint64, error) {
	w, containerID, err := r.workerForHandle(h)
	if err != nil {
		return 0, 0, err
	}
	client, base, err := r.dialer.DialWorker(w)
	if err != nil {
		return 0, 0, err
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/v1/replicas/%s/stats", base, containerID), nil)
	resp, err := client.Do(req)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, 0, fmt.Errorf("worker stats returned %d", resp.StatusCode)
	}
	var out api.StatsResult
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0, 0, err
	}
	return out.CPUPercent, out.RSSBytes, nil
}

func (r *remoteRuntime) RunOnce(ctx context.Context, p process.StartParams, logWriter io.Writer) (process.ExitInfo, error) {
	w, err := r.liveWorker()
	if err != nil {
		return process.ExitInfo{}, err
	}
	client, base, err := r.dialer.DialWorker(w)
	if err != nil {
		return process.ExitInfo{}, err
	}
	body, _ := json.Marshal(toStartRequest(p))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/replicas/run-once", bytes.NewReader(body))
	if err != nil {
		return process.ExitInfo{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return process.ExitInfo{}, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return process.ExitInfo{}, fmt.Errorf("worker run-once returned %d", resp.StatusCode)
	}
	payload, err := streamFrames(resp.Body, logWriter)
	if err != nil {
		return process.ExitInfo{}, err
	}
	var out api.ExitResult
	if err := json.Unmarshal(payload, &out); err != nil {
		return process.ExitInfo{}, err
	}
	return process.ExitInfo{Code: out.Code, Signaled: out.Signaled}, nil
}

var (
	_ process.Runtime            = (*remoteRuntime)(nil)
	_ process.ReplicaTransporter = (*remoteRuntime)(nil)
)
