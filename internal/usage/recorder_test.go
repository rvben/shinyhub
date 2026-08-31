package usage

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/proxy"
)

type recorderStore struct {
	mu            sync.Mutex
	beginErr      error
	beginFailures int
	starts        []db.UsageSessionStart
	ends          []string
	heartbeats    [][]string
}

func TestRecorderConnectionFastPathUsesCachedPolicy(t *testing.T) {
	policy := &Policy{
		Mode:       config.UsageIdentityUnattributed,
		generation: 7,
		overrides:  map[string]string{},
		// A nil store makes any accidental durable lookup on StartSession panic.
	}
	recorder := NewRecorderWithPolicy(&recorderStore{}, "cp", policy)
	if id := recorder.StartSession(proxy.UsageSessionStart{Slug: "sales"}); id == "" {
		t.Fatal("cached enabled policy rejected the session")
	}
	policy.mu.Lock()
	policy.overrides["sales"] = "disabled"
	policy.mu.Unlock()
	if id := recorder.StartSession(proxy.UsageSessionStart{Slug: "sales"}); id != "" {
		t.Fatal("cached disabled policy accepted the session")
	}
}

type recorderMetrics struct {
	mu     sync.Mutex
	events []string
}

func (m *recorderMetrics) RecordUsagePersistenceEvent(result string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, result)
}

func (s *recorderStore) BeginUsageSession(start db.UsageSessionStart) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.starts = append(s.starts, start)
	if s.beginFailures != 0 {
		if s.beginFailures > 0 {
			s.beginFailures--
		}
		return s.beginErr
	}
	return nil
}

func (s *recorderStore) BeginUsageSessionWithPolicy(start db.UsageSessionStart) (bool, error) {
	if err := s.BeginUsageSession(start); err != nil {
		return false, err
	}
	return true, nil
}

func (s *recorderStore) HeartbeatUsageSessions(ids []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.heartbeats = append(s.heartbeats, append([]string(nil), ids...))
	return nil
}

func (s *recorderStore) EndUsageSession(id string, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ends = append(s.ends, id)
	return nil
}

func TestRecorderPersistsAcceptedLifecycleInOrder(t *testing.T) {
	store := &recorderStore{}
	recorder := NewRecorder(store, "control-plane-a")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { recorder.Run(ctx); close(done) }()

	startedAt := time.Now().UTC().Add(-time.Minute)
	id := recorder.StartSession(proxy.UsageSessionStart{
		Slug: "sales", DeploymentID: 42, UserID: 7, StartedAt: startedAt,
	})
	if id == "" {
		t.Fatal("start event was not accepted")
	}
	recorder.EndSession(id)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("recorder did not drain on cancellation")
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.starts) != 1 || len(store.ends) != 1 || store.ends[0] != id {
		t.Fatalf("starts=%+v ends=%+v", store.starts, store.ends)
	}
	start := store.starts[0]
	if start.ID != id || start.Slug != "sales" || start.DeploymentID != 42 ||
		start.UserID != 7 || start.InstanceID != "control-plane-a" || !start.StartedAt.Equal(startedAt) {
		t.Fatalf("start = %+v", start)
	}
}

func TestRecorderHeartbeatBatchesActiveSessions(t *testing.T) {
	store := &recorderStore{}
	recorder := NewRecorder(store, "cp")
	active := make(map[string]struct{}, maxHeartbeatBatch+1)
	for i := 0; i < maxHeartbeatBatch+1; i++ {
		active[string(rune(i+1))] = struct{}{}
	}
	recorder.heartbeat(active)
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.heartbeats) != 2 || len(store.heartbeats[0]) != maxHeartbeatBatch || len(store.heartbeats[1]) != 1 {
		t.Fatalf("heartbeat batch sizes = %d/%d", len(store.heartbeats[0]), len(store.heartbeats[1]))
	}
}

func TestRecorderDropsEndAfterRejectedStart(t *testing.T) {
	store := &recorderStore{beginErr: db.ErrNotFound, beginFailures: -1}
	recorder := NewRecorder(store, "cp")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { recorder.Run(ctx); close(done) }()

	id := recorder.StartSession(proxy.UsageSessionStart{Slug: "sales", StartedAt: time.Now().UTC()})
	if id == "" {
		t.Fatal("start event was not accepted")
	}
	recorder.EndSession(id)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("recorder did not drain on cancellation")
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.starts) != 1 || len(store.ends) != 0 {
		t.Fatalf("starts=%d ends=%d", len(store.starts), len(store.ends))
	}
	if _, pending := recorder.pendingEnds.Load(id); pending {
		t.Fatal("rejected start left an end retry behind")
	}
}

func TestRecorderRetriesTransientStartBeforeClosingSession(t *testing.T) {
	store := &recorderStore{beginErr: errors.New("database unavailable"), beginFailures: 1}
	recorder := NewRecorder(store, "cp")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { recorder.Run(ctx); close(done) }()

	id := recorder.StartSession(proxy.UsageSessionStart{Slug: "sales", StartedAt: time.Now().UTC()})
	recorder.EndSession(id)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("recorder did not drain on cancellation")
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.starts) != 2 || len(store.ends) != 1 || store.ends[0] != id {
		t.Fatalf("starts=%d ends=%v", len(store.starts), store.ends)
	}
}

func TestRecorderUsesBoundedOverflowAndReportsLoss(t *testing.T) {
	recorder := NewRecorder(&recorderStore{}, "cp")
	metrics := &recorderMetrics{}
	recorder.SetMetrics(metrics)
	for i := 0; i < queueCapacity; i++ {
		recorder.events <- event{}
	}
	if id := recorder.StartSession(proxy.UsageSessionStart{Slug: "sales"}); id == "" {
		t.Fatal("overflow buffer rejected a short burst")
	}
	for i := 1; i < overflowCapacity; i++ {
		recorder.overflow <- event{}
	}
	if id := recorder.StartSession(proxy.UsageSessionStart{Slug: "sales"}); id != "" {
		t.Fatal("fully saturated bounded buffers accepted another start")
	}
	if recorder.dropped.Load() != 1 {
		t.Fatalf("dropped=%d, want 1", recorder.dropped.Load())
	}
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	if len(metrics.events) != 2 || metrics.events[0] != "start_overflow" || metrics.events[1] != "start_dropped" {
		t.Fatalf("metric events=%v", metrics.events)
	}
}
