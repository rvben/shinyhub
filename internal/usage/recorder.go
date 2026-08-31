// Package usage turns the proxy's WebSocket lifecycle callbacks into durable,
// bounded app-session analytics. Policy resolution is an in-memory lookup after
// a successful upgrade; session persistence stays off the serving path and
// atomically rechecks the durable privacy ceiling before writing.
package usage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/proxy"
)

const (
	queueCapacity        = 4096
	overflowCapacity     = 1024
	heartbeatEvery       = 30 * time.Second
	maxHeartbeatBatch    = 500
	maxStartAttempts     = 3
	maxPendingStartRetry = 4096
)

type store interface {
	BeginUsageSession(db.UsageSessionStart) error
	BeginUsageSessionWithPolicy(db.UsageSessionStart) (bool, error)
	HeartbeatUsageSessions([]string) error
	EndUsageSession(string, time.Time) error
}

type eventKind uint8

const (
	eventStart eventKind = iota + 1
	eventEnd
)

type event struct {
	kind             eventKind
	id               string
	start            proxy.UsageSessionStart
	identityMode     config.UsageIdentityMode
	policyGeneration int64
	ended            time.Time
	tries            uint8
}

// Metrics exposes bounded-vocabulary usage pipeline health without coupling
// the recorder to Prometheus.
type Metrics interface {
	RecordUsagePersistenceEvent(result string)
}

// Recorder is a bounded bridge between live proxy connections and the
// database. After its authoritative policy read, a saturated persistence queue
// drops analytics rather than applying backpressure to the live connection.
type Recorder struct {
	store         store
	instanceID    string
	events        chan event
	overflow      chan event
	dropped       atomic.Uint64
	pendingEnds   sync.Map // session id -> observed close time; fallback when the queue is full
	pendingStarts sync.Map // session id -> start event awaiting a bounded retry
	pendingCount  atomic.Int64
	failedStarts  sync.Map // session id -> struct{}; prevents retrying an end for a rejected start
	metrics       atomic.Pointer[metricsCallbacks]
	policy        *Policy
}

type metricsCallbacks struct{ record func(string) }

func NewRecorder(s store, instanceID string) *Recorder {
	return &Recorder{
		store: s, instanceID: instanceID,
		events: make(chan event, queueCapacity), overflow: make(chan event, overflowCapacity),
	}
}

// NewRecorderWithPolicy enables write-time privacy enforcement. The legacy
// constructor remains for focused recorder tests; production always supplies a
// durable policy loaded before the proxy begins serving.
func NewRecorderWithPolicy(s store, instanceID string, policy *Policy) *Recorder {
	r := NewRecorder(s, instanceID)
	r.policy = policy
	return r
}

// SetMetrics installs optional pipeline-health telemetry. It is safe to call
// before serving begins or while the recorder is running.
func (r *Recorder) SetMetrics(m Metrics) {
	if m == nil {
		r.metrics.Store(nil)
		return
	}
	r.metrics.Store(&metricsCallbacks{record: m.RecordUsagePersistenceEvent})
}

func (r *Recorder) record(result string) {
	if callbacks := r.metrics.Load(); callbacks != nil {
		callbacks.record(result)
	}
}

func newID() (string, bool) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		slog.Error("usage_session_id_failed", "err", err)
		return "", false
	}
	return hex.EncodeToString(b[:]), true
}

// StartSession implements proxy.UsageRecorder. It reads the last refreshed
// privacy snapshot in memory before enqueueing otherwise-asynchronous
// persistence; the durable insert clamps identity against current policy again.
func (r *Recorder) StartSession(start proxy.UsageSessionStart) string {
	id, ok := newID()
	if !ok {
		return ""
	}
	ev := event{kind: eventStart, id: id, start: start}
	if r.policy != nil {
		snapshot := r.policy.CachedSnapshot(start.Slug)
		if !snapshot.Collect {
			return ""
		}
		ev.identityMode = snapshot.IdentityMode
		ev.policyGeneration = snapshot.Generation
	}
	select {
	case r.events <- ev:
		return id
	default:
		select {
		case r.overflow <- ev:
			r.record("start_overflow")
			return id
		default:
			r.dropped.Add(1)
			r.record("start_dropped")
			return ""
		}
	}
}

// EndSession implements proxy.UsageRecorder and returns immediately.
func (r *Recorder) EndSession(id string) {
	if id == "" {
		return
	}
	select {
	case r.events <- event{kind: eventEnd, id: id, ended: time.Now().UTC()}:
	default:
		// Never lose an accepted session's close: otherwise the recorder would
		// keep heartbeating it forever. The run loop retries this side channel.
		r.pendingEnds.Store(id, time.Now().UTC())
	}
}

func (r *Recorder) handle(ev event, active map[string]struct{}) {
	switch ev.kind {
	case eventStart:
		principalKind := ev.start.PrincipalKind
		if principalKind == "" {
			if ev.start.UserID > 0 {
				principalKind = "person"
			} else {
				principalKind = "anonymous"
			}
		}
		identityMode := ev.identityMode
		generation := ev.policyGeneration
		if identityMode == "" {
			identityMode = config.UsageIdentityIdentified
		}
		if generation <= 0 {
			generation = 1
		}
		userID := ev.start.UserID
		viewerKey := ""
		if r.policy != nil {
			if principalKind == "person" && userID > 0 {
				viewerKey = r.policy.Pseudonym(ev.start.Slug, userID)
			}
		}
		start := db.UsageSessionStart{
			ID: ev.id, Slug: ev.start.Slug, DeploymentID: ev.start.DeploymentID,
			UserID: userID, ViewerKey: viewerKey, PrincipalKind: principalKind,
			IdentityMode: string(identityMode), PolicyGeneration: generation,
			InstanceID: r.instanceID, StartedAt: ev.start.StartedAt,
		}
		stored := true
		var err error
		if r.policy != nil {
			stored, err = r.store.BeginUsageSessionWithPolicy(start)
		} else {
			err = r.store.BeginUsageSession(start)
		}
		if err != nil {
			r.retryOrFailStart(ev, err)
			return
		}
		if !stored {
			r.pendingStarts.Delete(ev.id)
			r.failedStarts.Store(ev.id, struct{}{})
			return
		}
		r.pendingStarts.Delete(ev.id)
		active[ev.id] = struct{}{}
		if ended, pending := r.pendingEnds.Load(ev.id); pending {
			if closedAt, ok := ended.(time.Time); ok {
				r.finish(ev.id, closedAt, active)
			}
		}
	case eventEnd:
		r.finish(ev.id, ev.ended, active)
	}
}

func (r *Recorder) retryOrFailStart(ev event, err error) {
	if !errors.Is(err, db.ErrNotFound) && int(ev.tries)+1 < maxStartAttempts && r.deferStart(ev) {
		r.record("start_retry")
		slog.Warn("usage_session_start_retry", "slug", ev.start.Slug, "attempt", ev.tries+1, "err", err)
		return
	}
	r.failedStarts.Store(ev.id, struct{}{})
	r.record("start_failed")
	slog.Warn("usage_session_start_failed", "slug", ev.start.Slug, "err", err)
}

func (r *Recorder) deferStart(ev event) bool {
	if r.pendingCount.Load() >= maxPendingStartRetry {
		return false
	}
	ev.tries++
	if _, loaded := r.pendingStarts.LoadOrStore(ev.id, ev); loaded {
		r.pendingStarts.Store(ev.id, ev)
		return true
	}
	r.pendingCount.Add(1)
	return true
}

func (r *Recorder) flushPendingStarts(active map[string]struct{}) {
	r.pendingStarts.Range(func(key, value any) bool {
		id, idOK := key.(string)
		ev, eventOK := value.(event)
		if idOK {
			r.pendingStarts.Delete(id)
			r.pendingCount.Add(-1)
		}
		if idOK && eventOK {
			r.handle(ev, active)
		}
		return true
	})
}

func (r *Recorder) finish(id string, ended time.Time, active map[string]struct{}) {
	if _, failed := r.failedStarts.LoadAndDelete(id); failed {
		r.pendingEnds.Delete(id)
		return
	}
	_, wasActive := active[id]
	delete(active, id) // a closed socket must never receive another heartbeat
	if err := r.store.EndUsageSession(id, ended); err != nil {
		// If an accepted start once existed but its row has since disappeared
		// (for example, its app was deleted), there is nothing left to close.
		if wasActive && errors.Is(err, db.ErrNotFound) {
			r.pendingEnds.Delete(id)
			return
		}
		r.pendingEnds.Store(id, ended)
		r.record("end_retry")
		slog.Warn("usage_session_end_failed", "session_id", id, "err", err)
		return
	}
	r.pendingEnds.Delete(id)
}

func (r *Recorder) flushPendingEnds(active map[string]struct{}) {
	r.pendingEnds.Range(func(key, value any) bool {
		id, idOK := key.(string)
		ended, timeOK := value.(time.Time)
		if idOK && timeOK {
			r.finish(id, ended, active)
		} else {
			r.pendingEnds.Delete(key)
		}
		return true
	})
}

func (r *Recorder) heartbeat(active map[string]struct{}) {
	if r.policy != nil {
		if err := r.policy.Refresh(); err != nil {
			r.record("policy_refresh_failed")
			slog.Warn("usage_policy_refresh_failed", "err", err)
		}
	}
	r.flushPendingStarts(active)
	r.flushPendingEnds(active)
	if dropped := r.dropped.Swap(0); dropped > 0 {
		slog.Warn("usage_events_dropped", "count", dropped, "queue_capacity", queueCapacity)
	}
	if len(active) == 0 {
		return
	}
	batch := make([]string, 0, min(len(active), maxHeartbeatBatch))
	flush := func() {
		if len(batch) == 0 {
			return
		}
		if err := r.store.HeartbeatUsageSessions(batch); err != nil {
			slog.Warn("usage_session_heartbeat_failed", "count", len(batch), "err", err)
		}
		batch = batch[:0]
	}
	for id := range active {
		batch = append(batch, id)
		if len(batch) == maxHeartbeatBatch {
			flush()
		}
	}
	flush()
}

// Run processes lifecycle events serially so a close cannot overtake its start.
// On cancellation it drains events already accepted by the queue before
// returning; callers stop it only after upgraded connections have drained.
func (r *Recorder) Run(ctx context.Context) {
	active := make(map[string]struct{})
	ticker := time.NewTicker(heartbeatEvery)
	defer ticker.Stop()
	for {
		select {
		case ev := <-r.events:
			r.handle(ev, active)
		case ev := <-r.overflow:
			r.handle(ev, active)
		case <-ticker.C:
			r.heartbeat(active)
		case <-ctx.Done():
			for {
				select {
				case ev := <-r.events:
					r.handle(ev, active)
				case ev := <-r.overflow:
					r.handle(ev, active)
				default:
					for i := 0; i < maxStartAttempts && r.pendingCount.Load() > 0; i++ {
						r.flushPendingStarts(active)
					}
					r.flushPendingEnds(active)
					return
				}
			}
		}
	}
}

var _ proxy.UsageRecorder = (*Recorder)(nil)
