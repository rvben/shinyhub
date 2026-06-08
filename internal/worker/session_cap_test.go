package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/worker/api"
)

// captureHandler is an slog.Handler that captures all records emitted at Warn
// or above for assertions in tests.
type captureHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *captureHandler) Enabled(_ context.Context, lvl slog.Level) bool {
	return lvl >= slog.LevelWarn
}
func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	h.records = append(h.records, r)
	h.mu.Unlock()
	return nil
}
func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(string) slog.Handler      { return h }
func (h *captureHandler) hasMessage(substr string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.records {
		if strings.Contains(r.Message, substr) {
			return true
		}
	}
	return false
}

// doDataReq fires a single GET /v1/data/tok/health at srv and returns the HTTP
// status. It requires a replicaRecord registered under srv.byToken["tok"]; tests
// that re-adopt containers (which mint their own tokens) must not use this helper.
func doDataReq(ctx context.Context, srv *replicaServer) int {
	req := httptest.NewRequest(http.MethodGet, "/v1/data/tok/health", nil).WithContext(ctx)
	req = withURLParam(req, "token", "tok")
	req = withURLParam(req, "*", "health")
	rw := httptest.NewRecorder()
	srv.handleData(rw, req)
	return rw.Code
}

// waitConns polls rec.activeConns until it equals target or 2 s elapses.
func waitConns(t *testing.T, rec *replicaRecord, target int64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for rec.activeConns.Load() != target {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for activeConns=%d; got %d", target, rec.activeConns.Load())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestSessionCap_OverCapReturns503 fills all N slots with blocking requests
// then asserts the (N+1)th is shed with 503.
func TestSessionCap_OverCapReturns503(t *testing.T) {
	const cap = 3

	// A blocking backend holds slots open for the duration of the test.
	// cancel must run before backend.Close to release the blocked goroutines.
	blockCtx, cancel := context.WithCancel(context.Background())
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(func() { cancel(); backend.Close() })
	u, _ := url.Parse(backend.URL)
	hostPort, _ := strconv.Atoi(u.Port())

	srv := NewReplicaServer(ReplicaServerConfig{
		Runtime:      &fakeRuntime{},
		DataDir:      t.TempDir(),
		NodeID:       "node-a",
		Advertise:    "w:8443",
		AllocatePort: func() int { return hostPort },
	})
	rec := &replicaRecord{token: "tok", containerID: "c-1", hostPort: hostPort, maxSessions: cap}
	srv.mu.Lock()
	srv.byToken["tok"] = rec
	srv.mu.Unlock()

	// Fill all cap slots with blocking goroutines.
	for i := 0; i < cap; i++ {
		go func() { doDataReq(blockCtx, srv) }()
	}
	waitConns(t, rec, int64(cap))

	// The (cap+1)th request must be shed.
	code := doDataReq(context.Background(), srv)
	if code != http.StatusServiceUnavailable {
		t.Errorf("over-cap request returned %d, want 503", code)
	}

	// Counter must not have leaked (blocked goroutines still hold cap slots).
	if n := rec.activeConns.Load(); n != int64(cap) {
		t.Errorf("activeConns after over-cap shed = %d, want %d", n, cap)
	}
}

// TestSessionCap_UnderCapIsServed asserts that requests below the cap are
// proxied and the counter returns to 0 after each completes.
func TestSessionCap_UnderCapIsServed(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(backend.Close)
	u, _ := url.Parse(backend.URL)
	hostPort, _ := strconv.Atoi(u.Port())

	srv := NewReplicaServer(ReplicaServerConfig{
		Runtime:      &fakeRuntime{},
		DataDir:      t.TempDir(),
		NodeID:       "node-a",
		Advertise:    "w:8443",
		AllocatePort: func() int { return hostPort },
	})
	rec := &replicaRecord{token: "tok", containerID: "c-1", hostPort: hostPort, maxSessions: 5}
	srv.mu.Lock()
	srv.byToken["tok"] = rec
	srv.mu.Unlock()

	for i := 0; i < 5; i++ {
		code := doDataReq(context.Background(), srv)
		if code != http.StatusOK {
			t.Errorf("request %d: status = %d, want 200", i, code)
		}
	}
	if n := rec.activeConns.Load(); n != 0 {
		t.Errorf("activeConns after sequential requests = %d, want 0", n)
	}
}

// TestSessionCap_SlotFreesAfterRequest asserts that when an in-flight request
// finishes, the freed slot admits the next request.
func TestSessionCap_SlotFreesAfterRequest(t *testing.T) {
	const cap = 1

	// Phase 1: blocking backend to occupy the single slot.
	// unblock must be called before blockBackend.Close.
	blockCtx, unblock := context.WithCancel(context.Background())
	blockBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(func() { unblock(); blockBackend.Close() })
	ub, _ := url.Parse(blockBackend.URL)
	blockPort, _ := strconv.Atoi(ub.Port())

	srv := NewReplicaServer(ReplicaServerConfig{
		Runtime:      &fakeRuntime{},
		DataDir:      t.TempDir(),
		NodeID:       "node-a",
		Advertise:    "w:8443",
		AllocatePort: func() int { return blockPort },
	})
	rec := &replicaRecord{token: "tok", containerID: "c-1", hostPort: blockPort, maxSessions: cap}
	srv.mu.Lock()
	srv.byToken["tok"] = rec
	srv.mu.Unlock()

	// Occupy the single slot.
	go func() { doDataReq(blockCtx, srv) }()
	waitConns(t, rec, 1)

	// Over-cap: must be shed.
	if code := doDataReq(context.Background(), srv); code != http.StatusServiceUnavailable {
		t.Errorf("over-cap status = %d, want 503", code)
	}

	// Release the blocking slot.
	unblock()
	waitConns(t, rec, 0)

	// Phase 2: immediate backend now that the slot is free.
	okBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(okBackend.Close)
	uo, _ := url.Parse(okBackend.URL)
	okPort, _ := strconv.Atoi(uo.Port())
	rec.hostPort = okPort

	if code := doDataReq(context.Background(), srv); code != http.StatusOK {
		t.Errorf("post-free status = %d, want 200", code)
	}
}

// TestSessionCap_ZeroMeansUnlimited asserts that maxSessions=0 never 503s.
func TestSessionCap_ZeroMeansUnlimited(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(backend.Close)
	u, _ := url.Parse(backend.URL)
	hostPort, _ := strconv.Atoi(u.Port())

	srv := NewReplicaServer(ReplicaServerConfig{
		Runtime:      &fakeRuntime{},
		DataDir:      t.TempDir(),
		NodeID:       "node-a",
		Advertise:    "w:8443",
		AllocatePort: func() int { return hostPort },
	})
	rec := &replicaRecord{token: "tok", containerID: "c-1", hostPort: hostPort, maxSessions: 0}
	srv.mu.Lock()
	srv.byToken["tok"] = rec
	srv.mu.Unlock()

	var wg sync.WaitGroup
	shed := make(chan struct{}, 100)
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if code := doDataReq(context.Background(), srv); code != http.StatusOK {
				shed <- struct{}{}
			}
		}()
	}
	wg.Wait()
	close(shed)
	if len(shed) > 0 {
		t.Errorf("unlimited cap shed %d requests, want 0", len(shed))
	}
}

// TestSessionCap_RaceOnCounter exercises the atomic counter under -race.
func TestSessionCap_RaceOnCounter(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(backend.Close)
	u, _ := url.Parse(backend.URL)
	hostPort, _ := strconv.Atoi(u.Port())

	srv := NewReplicaServer(ReplicaServerConfig{
		Runtime:      &fakeRuntime{},
		DataDir:      t.TempDir(),
		NodeID:       "node-a",
		Advertise:    "w:8443",
		AllocatePort: func() int { return hostPort },
	})
	rec := &replicaRecord{token: "tok", containerID: "c-1", hostPort: hostPort, maxSessions: 5}
	srv.mu.Lock()
	srv.byToken["tok"] = rec
	srv.mu.Unlock()

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			doDataReq(context.Background(), srv)
		}()
	}
	wg.Wait()
	if n := rec.activeConns.Load(); n != 0 {
		t.Errorf("activeConns after all requests = %d, want 0", n)
	}
}

// TestRebuildFromContainers_WithCapLabel asserts that a re-adopted container
// whose shinyhub.max_sessions label is set enforces that cap after rebuild.
func TestRebuildFromContainers_WithCapLabel(t *testing.T) {
	// A blocking backend to hold slots open for the enforcement check.
	// cancel must run before blockBackend.Close to release the blocked goroutines.
	blockCtx, cancel := context.WithCancel(context.Background())
	blockBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(func() { cancel(); blockBackend.Close() })
	ub, _ := url.Parse(blockBackend.URL)
	blockPort, _ := strconv.Atoi(ub.Port())

	rt := &fakeLister{
		containers: []process.ContainerInfo{
			{ID: "c-1", Labels: map[string]string{
				"shinyhub.slug":          "app",
				"shinyhub.replica_index": "0",
				"shinyhub.max_sessions":  "2",
			}},
		},
		hostPorts: map[string]int{"c-1": blockPort},
	}
	srv := NewReplicaServer(ReplicaServerConfig{
		Runtime:      rt,
		DataDir:      t.TempDir(),
		NodeID:       "node-a",
		Advertise:    "w:8443",
		AllocatePort: func() int { return blockPort },
	})

	if err := srv.RebuildFromContainers(); err != nil {
		t.Fatalf("RebuildFromContainers: %v", err)
	}

	srv.mu.RLock()
	rec, ok := srv.byContainer["c-1"]
	srv.mu.RUnlock()
	if !ok {
		t.Fatal("c-1 not re-adopted")
	}
	if rec.maxSessions != 2 {
		t.Errorf("maxSessions = %d, want 2", rec.maxSessions)
	}

	// Use the minted token from the re-adopted record (not the hardcoded "tok").
	doCapDataReq := func(ctx context.Context) int {
		srv.mu.RLock()
		tok := rec.token
		srv.mu.RUnlock()
		req := httptest.NewRequest(http.MethodGet, "/v1/data/"+tok+"/health", nil).WithContext(ctx)
		req = withURLParam(req, "token", tok)
		req = withURLParam(req, "*", "health")
		rw := httptest.NewRecorder()
		srv.handleData(rw, req)
		return rw.Code
	}

	// Fill 2 slots with blocking requests.
	for i := 0; i < 2; i++ {
		go func() { doCapDataReq(blockCtx) }()
	}
	waitConns(t, rec, 2)

	// 3rd request must be shed.
	code := doCapDataReq(context.Background())
	if code != http.StatusServiceUnavailable {
		t.Errorf("over-cap on re-adopted container returned %d, want 503", code)
	}
}

// TestRebuildFromContainers_MissingCapLabel asserts that a re-adopted container
// lacking the shinyhub.max_sessions label serves uncapped and logs a warning.
func TestRebuildFromContainers_MissingCapLabel(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(backend.Close)
	u, _ := url.Parse(backend.URL)
	hostPort, _ := strconv.Atoi(u.Port())

	rt := &fakeLister{
		containers: []process.ContainerInfo{
			{ID: "c-1", Labels: map[string]string{
				"shinyhub.slug":          "app",
				"shinyhub.replica_index": "0",
				// No shinyhub.max_sessions label.
			}},
		},
		hostPorts: map[string]int{"c-1": hostPort},
	}
	srv := NewReplicaServer(ReplicaServerConfig{
		Runtime:      rt,
		DataDir:      t.TempDir(),
		NodeID:       "node-a",
		Advertise:    "w:8443",
		AllocatePort: func() int { return hostPort },
	})

	// Install a capturing slog handler to assert the warning fires.
	ch := &captureHandler{}
	old := slog.Default()
	slog.SetDefault(slog.New(ch))
	t.Cleanup(func() { slog.SetDefault(old) })

	if err := srv.RebuildFromContainers(); err != nil {
		t.Fatalf("RebuildFromContainers: %v", err)
	}

	srv.mu.RLock()
	rec, ok := srv.byContainer["c-1"]
	srv.mu.RUnlock()
	if !ok {
		t.Fatal("c-1 not re-adopted")
	}
	tok := rec.token
	if rec.maxSessions != 0 {
		t.Errorf("maxSessions = %d, want 0 (uncapped)", rec.maxSessions)
	}
	if !ch.hasMessage("no max_sessions label") {
		t.Error("expected slog.Warn about missing max_sessions label, none found")
	}

	// Requests must be served (not 503'd) with no cap.
	req := httptest.NewRequest(http.MethodGet, "/v1/data/"+tok+"/health", nil)
	req = withURLParam(req, "token", tok)
	req = withURLParam(req, "*", "health")
	rw := httptest.NewRecorder()
	srv.handleData(rw, req)
	if rw.Code != http.StatusOK {
		t.Errorf("uncapped re-adopted container returned %d, want 200", rw.Code)
	}
}

// TestWire_ToStartRequestPopulatesMaxSessions asserts that toStartRequest copies
// MaxSessions from StartParams to the wire type.
func TestWire_ToStartRequestPopulatesMaxSessions(t *testing.T) {
	p := process.StartParams{
		Slug:        "app",
		Index:       0,
		Tier:        "remote",
		Command:     []string{"./server"},
		Port:        8080,
		MaxSessions: 7,
	}
	req := toStartRequest(p)
	if req.MaxSessions != 7 {
		t.Errorf("toStartRequest: MaxSessions = %d, want 7", req.MaxSessions)
	}

	// Zero is preserved (omitempty in JSON but the Go field stays zero).
	p.MaxSessions = 0
	req = toStartRequest(p)
	if req.MaxSessions != 0 {
		t.Errorf("toStartRequest with zero: MaxSessions = %d, want 0", req.MaxSessions)
	}
}

// TestBuildStartParams_MaxSessionsPersisted is the regression test for the bug
// where buildStartParams omitted MaxSessions from the returned StartParams,
// causing dockerLabels to never stamp shinyhub.max_sessions on worker-started
// containers and silently dropping the hard cap after an agent restart.
//
// It asserts two things:
//  1. buildStartParams returns StartParams.MaxSessions == reqBody.MaxSessions.
//  2. A handleStart call with MaxSessions=N results in the runtime receiving
//     StartParams.MaxSessions==N (which DockerRuntime then stamps as a label).
//
// This test MUST fail before the fix (MaxSessions omitted in buildStartParams)
// and MUST pass after it.
func TestBuildStartParams_MaxSessionsPersisted(t *testing.T) {
	// Part 1: buildStartParams propagates MaxSessions into StartParams.
	started := make(chan struct{})
	rt := &fakeRuntime{startURL: "http://127.0.0.1:49001", started: started}
	srv := NewReplicaServer(ReplicaServerConfig{
		Runtime:      rt,
		DataDir:      t.TempDir(),
		NodeID:       "node-a",
		Advertise:    "w:8443",
		AllocatePort: func() int { return 49001 },
	})

	params, err := srv.buildStartParams(context.Background(), api.ReplicaStartRequest{
		Slug:        "app",
		Index:       0,
		BindPort:    8000,
		MaxSessions: 9,
	}, 49001)
	if err != nil {
		t.Fatalf("buildStartParams: %v", err)
	}
	if params.MaxSessions != 9 {
		t.Errorf("buildStartParams: StartParams.MaxSessions = %d, want 9 (label will not be stamped without this)", params.MaxSessions)
	}

	// Part 2: the runtime receives MaxSessions so DockerRuntime can stamp the
	// label. Drive a full handleStart and assert rt.startParams.MaxSessions.
	body, _ := json.Marshal(api.ReplicaStartRequest{
		Slug:        "app",
		Index:       0,
		Command:     []string{"./server"},
		BindPort:    8080,
		MaxSessions: 9,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodPost, "/v1/replicas", bytes.NewReader(body)).WithContext(ctx)
	rw := httptest.NewRecorder()

	done := make(chan struct{})
	go func() { srv.handleStart(rw, req); close(done) }()

	// Wait until Start has been called (deterministic, no sleep).
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for runtime.Start to be called")
	}
	cancel()
	<-done

	if rt.startParams.MaxSessions != 9 {
		t.Errorf("runtime received StartParams.MaxSessions = %d, want 9; shinyhub.max_sessions label will not be stamped",
			rt.startParams.MaxSessions)
	}
}

// TestRebuildFromContainers_UnparseableCapLabel asserts that a re-adopted
// container whose shinyhub.max_sessions label is present but not a valid
// integer is treated as uncapped (maxSessions=0), re-adoption is not aborted,
// and a warning is logged.
func TestRebuildFromContainers_UnparseableCapLabel(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(backend.Close)
	u, _ := url.Parse(backend.URL)
	hostPort, _ := strconv.Atoi(u.Port())

	rt := &fakeLister{
		containers: []process.ContainerInfo{
			{ID: "c-1", Labels: map[string]string{
				"shinyhub.slug":          "app",
				"shinyhub.replica_index": "0",
				"shinyhub.max_sessions":  "not-a-number",
			}},
		},
		hostPorts: map[string]int{"c-1": hostPort},
	}
	srv := NewReplicaServer(ReplicaServerConfig{
		Runtime:      rt,
		DataDir:      t.TempDir(),
		NodeID:       "node-a",
		Advertise:    "w:8443",
		AllocatePort: func() int { return hostPort },
	})

	ch := &captureHandler{}
	old := slog.Default()
	slog.SetDefault(slog.New(ch))
	t.Cleanup(func() { slog.SetDefault(old) })

	if err := srv.RebuildFromContainers(); err != nil {
		t.Fatalf("RebuildFromContainers should not fail on an unparseable label: %v", err)
	}

	srv.mu.RLock()
	rec, ok := srv.byContainer["c-1"]
	srv.mu.RUnlock()
	if !ok {
		t.Fatal("c-1 not re-adopted")
	}
	tok := rec.token
	if rec.maxSessions != 0 {
		t.Errorf("maxSessions = %d, want 0 (uncapped) on unparseable label", rec.maxSessions)
	}
	if !ch.hasMessage("unparseable max_sessions label") {
		t.Error("expected slog.Warn about unparseable label, none found")
	}

	// Requests must still be served (not 503'd).
	req := httptest.NewRequest(http.MethodGet, "/v1/data/"+tok+"/health", nil)
	req = withURLParam(req, "token", tok)
	req = withURLParam(req, "*", "health")
	rw := httptest.NewRecorder()
	srv.handleData(rw, req)
	if rw.Code != http.StatusOK {
		t.Errorf("uncapped re-adopted container returned %d, want 200", rw.Code)
	}
}

// TestRebuildFromContainers_ZeroCapLabelIsUncappedNoWarn asserts that a
// parseable "0" for shinyhub.max_sessions means uncapped and does NOT log a
// warning (0 is a valid explicit "no cap" value).
func TestRebuildFromContainers_ZeroCapLabelIsUncappedNoWarn(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(backend.Close)
	u, _ := url.Parse(backend.URL)
	hostPort, _ := strconv.Atoi(u.Port())

	rt := &fakeLister{
		containers: []process.ContainerInfo{
			{ID: "c-1", Labels: map[string]string{
				"shinyhub.slug":          "app",
				"shinyhub.replica_index": "0",
				"shinyhub.max_sessions":  "0",
			}},
		},
		hostPorts: map[string]int{"c-1": hostPort},
	}
	srv := NewReplicaServer(ReplicaServerConfig{
		Runtime:      rt,
		DataDir:      t.TempDir(),
		NodeID:       "node-a",
		Advertise:    "w:8443",
		AllocatePort: func() int { return hostPort },
	})

	ch := &captureHandler{}
	old := slog.Default()
	slog.SetDefault(slog.New(ch))
	t.Cleanup(func() { slog.SetDefault(old) })

	if err := srv.RebuildFromContainers(); err != nil {
		t.Fatalf("RebuildFromContainers: %v", err)
	}

	srv.mu.RLock()
	rec, ok := srv.byContainer["c-1"]
	srv.mu.RUnlock()
	if !ok {
		t.Fatal("c-1 not re-adopted")
	}
	if rec.maxSessions != 0 {
		t.Errorf("maxSessions = %d, want 0", rec.maxSessions)
	}
	// A "0" label is valid and must NOT produce any warning.
	if ch.hasMessage("max_sessions") {
		t.Error("a parseable '0' label must not produce a warning")
	}
}

// TestWire_HandleStartPopulatesMaxSessionsOnRecord asserts that handleStart
// copies MaxSessions from the request body to the replicaRecord.
// Uses deterministic synchronization via the fakeRuntime.started channel
// instead of a fixed sleep.
func TestWire_HandleStartPopulatesMaxSessionsOnRecord(t *testing.T) {
	dir := t.TempDir()
	started := make(chan struct{})
	rt := &fakeRuntime{startURL: "http://127.0.0.1:49001", started: started}
	srv := NewReplicaServer(ReplicaServerConfig{
		Runtime:      rt,
		DataDir:      dir,
		NodeID:       "node-a",
		Advertise:    "w:8443",
		AllocatePort: func() int { return 49001 },
	})

	body, _ := json.Marshal(api.ReplicaStartRequest{
		Slug:        "app",
		Index:       0,
		Command:     []string{"./server"},
		BindPort:    8080,
		MaxSessions: 5,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodPost, "/v1/replicas", bytes.NewReader(body)).WithContext(ctx)
	rw := httptest.NewRecorder()

	done := make(chan struct{})
	go func() { srv.handleStart(rw, req); close(done) }()

	// Wait until Start has been called (deterministic, no sleep).
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for runtime.Start to be called")
	}
	cancel()
	<-done

	srv.mu.RLock()
	defer srv.mu.RUnlock()
	for _, r := range srv.byToken {
		if r.maxSessions != 5 {
			t.Errorf("replicaRecord.maxSessions = %d, want 5", r.maxSessions)
		}
		return
	}
	t.Error("no replicaRecord found after handleStart")
}
