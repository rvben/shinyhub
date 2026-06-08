package worker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"syscall"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/worker/api"
)

// ProviderRemoteDocker labels replicas started on a remote Docker worker.
const ProviderRemoteDocker = "remote_docker"

// startDialRetries and startDialRetryBackoff bound how long Start re-POSTs a
// replica-start request that was refused at connection time. A worker binds its
// data-plane listener just before its first heartbeat, but a deploy racing that
// heartbeat can dial it a moment too early; the listener then appears within
// about a second. Only a connection-establishment failure is retried (the
// request never reached the worker, so re-POSTing cannot double-start a
// container); any other error fails fast. 5 retries at 200ms is ~1s of cover.
const (
	startDialRetries      = 5
	startDialRetryBackoff = 200 * time.Millisecond
)

// WorkerLookup resolves workers for routing. Implemented by *Registry in
// production; a stub is used in tests. PlanPlacementForTier plans where to place
// a batch of new replicas of a slug (least-loaded, spread across the tier, one
// worker per replica); WorkersForTier enumerates every up worker on a tier
// (inventory spans all of them); Worker resolves the specific worker that owns
// an existing handle or a pre-planned target.
type WorkerLookup interface {
	PlanPlacementForTier(tier, slug string, count int) []db.Worker
	WorkersForTier(tier string) []db.Worker
	Worker(nodeID string) (db.Worker, bool)
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

// PlanPlacement plans worker node ids for count new replicas of slug across the
// tier, one per replica in assignment order, so deploy can pin a concurrent pool
// boot up front instead of each replica self-placing against the same snapshot.
// Returns nil when no worker is up on the tier; the caller then fails the boot
// closed via Start's self-placement.
func (r *remoteRuntime) PlanPlacement(slug string, count int) []string {
	ws := r.lookup.PlanPlacementForTier(r.tier, slug, count)
	if len(ws) == 0 {
		return nil
	}
	ids := make([]string, len(ws))
	for i, w := range ws {
		ids[i] = w.NodeID
	}
	return ids
}

// workerForStart resolves the worker to start a replica on. When the replica
// carries a pre-planned target worker (deploy pinned it for batch spread), it
// resolves and validates that worker, failing closed unless it is up and on this
// runtime's tier -- a stale target must never start a replica on a down or
// wrong-tier worker. With no target it self-places against live load, spreading
// across the tier's up workers, which is correct for a single-replica boot such
// as a watchdog restart.
func (r *remoteRuntime) workerForStart(p process.StartParams) (db.Worker, error) {
	if p.TargetWorker != "" {
		w, ok := r.lookup.Worker(p.TargetWorker)
		if !ok {
			return db.Worker{}, fmt.Errorf("target worker %q: %w", p.TargetWorker, process.ErrNoLiveWorker)
		}
		if w.Status != "up" {
			return db.Worker{}, fmt.Errorf("target worker %q is %s: %w", p.TargetWorker, w.Status, process.ErrNoLiveWorker)
		}
		if w.Tier != r.tier {
			return db.Worker{}, fmt.Errorf("target worker %q is on tier %q, not %q: %w", p.TargetWorker, w.Tier, r.tier, process.ErrNoLiveWorker)
		}
		return w, nil
	}
	return r.placeWorker(p.Slug)
}

// placeWorker selects the worker to place a new replica of slug on, spreading
// load across the tier's up workers, failing closed when none is live.
func (r *remoteRuntime) placeWorker(slug string) (db.Worker, error) {
	ws := r.lookup.PlanPlacementForTier(r.tier, slug, 1)
	if len(ws) == 0 {
		return db.Worker{}, fmt.Errorf("tier %q: %w", r.tier, process.ErrNoLiveWorker)
	}
	return ws[0], nil
}

// workerForHandle resolves the worker that owns an opaque handle by the handle's
// encoded node id, failing closed unless that worker is currently up. Resolving
// by the handle's own node (rather than the tier's routing worker) is correct
// under multi-worker placement, where a replica can live on any up worker on the
// tier; a dial to a down or unknown worker would hang or fail, so it is rejected.
func (r *remoteRuntime) workerForHandle(h process.RunHandle) (db.Worker, string, error) {
	nodeID, containerID, err := decodeRemoteHandle(h.ContainerID)
	if err != nil {
		return db.Worker{}, "", err
	}
	w, ok := r.lookup.Worker(nodeID)
	if !ok {
		return db.Worker{}, "", fmt.Errorf("handle node %q: %w", nodeID, process.ErrNoLiveWorker)
	}
	if w.Status != "up" {
		return db.Worker{}, "", fmt.Errorf("handle node %q is %s: %w", nodeID, w.Status, process.ErrNoLiveWorker)
	}
	// Constrain the resolved worker to this runtime's tier so a stale or
	// mismatched handle can never operate on another tier's worker.
	if w.Tier != r.tier {
		return db.Worker{}, "", fmt.Errorf("handle node %q is on tier %q, not %q: %w", nodeID, w.Tier, r.tier, process.ErrNoLiveWorker)
	}
	return w, containerID, nil
}

func (r *remoteRuntime) HostPreparesDeps() bool    { return false }
func (r *remoteRuntime) AppBindHost() string       { return "0.0.0.0" }
func (r *remoteRuntime) HostProvidesAppData() bool { return false }

// ReplicaTransportForWorker returns the mTLS transport for the named worker so
// the proxy and health paths reach that worker's data plane. It fails closed
// (nil) when the worker is unknown, not up, or not on this runtime's tier, so a
// route is never built with the wrong worker's transport; callers fall back to
// the default transport, which then fails the health check cleanly.
func (r *remoteRuntime) ReplicaTransportForWorker(nodeID string) http.RoundTripper {
	w, ok := r.lookup.Worker(nodeID)
	if !ok || w.Status != "up" || w.Tier != r.tier {
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
	// Convert KEY=VALUE env slices to the map the wire type uses. SecretEnv is
	// folded in alongside Env: a remote_docker container shares the worker's
	// trust boundary like local Docker, so secrets travel as plaintext env (no
	// behavior change). Keys are disjoint between the two slices.
	envMap := make(map[string]string, len(p.Env)+len(p.SecretEnv))
	putEnv := func(kv string) {
		if idx := strings.Index(kv, "="); idx > 0 {
			envMap[kv[:idx]] = kv[idx+1:]
		}
	}
	for _, kv := range p.Env {
		putEnv(kv)
	}
	for _, kv := range p.SecretEnv {
		putEnv(kv)
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
		MaxSessions:      p.MaxSessions,
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
	w, err := r.workerForStart(p)
	if err != nil {
		return process.ReplicaEndpoint{}, err
	}
	body, _ := json.Marshal(toStartRequest(p))

	for attempt := 0; ; attempt++ {
		ep, err := r.startOnce(ctx, w, body, logWriter)
		if err == nil {
			return ep, nil
		}
		// Retry only a refused connection: the request never reached the worker,
		// so re-POSTing cannot double-start a container. Any other failure (a
		// worker HTTP status, a mid-stream error, a non-dial network error) may
		// mean the request landed, so surface it immediately.
		if attempt >= startDialRetries || !isConnRefused(err) {
			return process.ReplicaEndpoint{}, err
		}
		select {
		case <-ctx.Done():
			return process.ReplicaEndpoint{}, ctx.Err()
		case <-time.After(startDialRetryBackoff):
		}
	}
}

// startOnce performs a single replica-start round trip: dial the worker, POST
// the request, and read the result frame. Each call builds a fresh request body
// reader so Start can re-invoke it on a retried connection refusal.
func (r *remoteRuntime) startOnce(ctx context.Context, w db.Worker, body []byte, logWriter io.Writer) (process.ReplicaEndpoint, error) {
	client, base, err := r.dialer.DialWorker(w)
	if err != nil {
		return process.ReplicaEndpoint{}, err
	}
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

// isConnRefused reports whether err is a connection-establishment refusal
// (ECONNREFUSED), the one failure mode where the start request provably never
// reached the worker and is therefore safe to retry. A connection reset or any
// post-connect error is excluded: the request may already have started a
// container.
func isConnRefused(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED)
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
	w, err := r.workerForStart(p)
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
	_ process.ReplicaInventory   = (*remoteRuntime)(nil)
	_ process.ReplicaPlacer      = (*remoteRuntime)(nil)
)
