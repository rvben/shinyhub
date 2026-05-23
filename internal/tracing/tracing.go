// Package tracing implements lightweight W3C-trace-context propagation through
// the reverse proxy and a per-app ring buffer of recent slow/error proxy spans
// for the UI's Traces tab. It also computes the OTEL_* environment variables
// injected into each app process so Shiny's built-in OpenTelemetry exporter
// reaches the operator's chosen backend without per-app configuration.
//
// Design choice: ShinyHub does NOT pull in the full OpenTelemetry Go SDK. The
// proxy is on the hot path for every request; we only need to (a) parse/emit
// the `traceparent` header (a fixed-width hex string per W3C trace context),
// (b) generate IDs when no upstream context exists, and (c) decide locally
// whether to retain a request in the ring buffer. Apps export their own spans
// directly to the configured OTLP endpoint; ShinyHub never sees the bytes.
package tracing

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/binary"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/rvben/shinyhub/internal/config"
)

// TraceContext is a parsed W3C traceparent value plus the propagated tracestate.
//
// Per the W3C spec, traceparent is "00-<32 hex trace_id>-<16 hex span_id>-<2 hex flags>".
// We only track the sampled flag (0x01); other flag bits are preserved on
// re-emission via the raw Flags byte.
type TraceContext struct {
	TraceID    [16]byte
	SpanID     [8]byte
	Flags      byte
	TraceState string
}

// Sampled reports whether the W3C sampled flag is set.
func (c TraceContext) Sampled() bool { return c.Flags&0x01 != 0 }

// TraceparentHeader returns the wire-format `traceparent` value for this context.
func (c TraceContext) TraceparentHeader() string {
	return fmt.Sprintf("00-%s-%s-%02x", hex.EncodeToString(c.TraceID[:]), hex.EncodeToString(c.SpanID[:]), c.Flags)
}

// TraceIDHex returns the lowercase hex form of the trace ID, suitable for
// substitution into trace_link_template.
func (c TraceContext) TraceIDHex() string { return hex.EncodeToString(c.TraceID[:]) }

// ParseTraceparent parses a W3C traceparent header value. Returns ok=false for
// any malformed input — callers should treat that as "no upstream context" and
// start a fresh trace.
func ParseTraceparent(v string) (TraceContext, bool) {
	if len(v) < 55 {
		return TraceContext{}, false
	}
	parts := strings.Split(v, "-")
	if len(parts) != 4 {
		return TraceContext{}, false
	}
	if parts[0] != "00" {
		return TraceContext{}, false
	}
	if len(parts[1]) != 32 || len(parts[2]) != 16 || len(parts[3]) != 2 {
		return TraceContext{}, false
	}
	var ctx TraceContext
	tid, err := hex.DecodeString(parts[1])
	if err != nil {
		return TraceContext{}, false
	}
	sid, err := hex.DecodeString(parts[2])
	if err != nil {
		return TraceContext{}, false
	}
	flags, err := hex.DecodeString(parts[3])
	if err != nil {
		return TraceContext{}, false
	}
	// The all-zero trace ID and span ID are invalid per spec.
	if isZero(tid) || isZero(sid) {
		return TraceContext{}, false
	}
	copy(ctx.TraceID[:], tid)
	copy(ctx.SpanID[:], sid)
	ctx.Flags = flags[0]
	return ctx, true
}

func isZero(b []byte) bool {
	for _, x := range b {
		if x != 0 {
			return false
		}
	}
	return true
}

// NewTraceID returns a random non-zero trace ID.
func NewTraceID() [16]byte {
	var id [16]byte
	for {
		_, _ = rand.Read(id[:])
		if !isZero(id[:]) {
			return id
		}
	}
}

// NewSpanID returns a random non-zero span ID.
func NewSpanID() [8]byte {
	var id [8]byte
	for {
		_, _ = rand.Read(id[:])
		if !isZero(id[:]) {
			return id
		}
	}
}

// SampleByTraceID returns true when the trace ID's leading bits, interpreted as
// a uint64, fall below ratio*2^64. This is the standard OTel traceidratio
// algorithm: a deterministic decision so distributed components agree.
func SampleByTraceID(id [16]byte, ratio float64) bool {
	if ratio <= 0 {
		return false
	}
	if ratio >= 1 {
		return true
	}
	// First 8 bytes interpreted big-endian as a uint64.
	v := binary.BigEndian.Uint64(id[:8])
	threshold := uint64(ratio * (1 << 63) * 2)
	return v < threshold
}

// Span is a record of one proxy-handled request retained in the ring buffer.
//
// Note: this is NOT an OpenTelemetry span — it's the slim subset of a span we
// need for the UI's Traces tab. Apps emit real OTel spans directly to the OTLP
// endpoint; ShinyHub's role is only to propagate context and surface its own
// proxy-level view.
type Span struct {
	TraceID    string    `json:"trace_id"`
	SpanID     string    `json:"span_id"`
	ParentID   string    `json:"parent_id,omitempty"`
	AppSlug    string    `json:"app_slug"`
	Replica    int       `json:"replica"`
	Method     string    `json:"method"`
	Path       string    `json:"path"`
	Status     int       `json:"status"`
	DurationMS int64     `json:"duration_ms"`
	StartedAt  time.Time `json:"started_at"`
	Sampled    bool      `json:"sampled"`
	Error      string    `json:"error,omitempty"`
}

// Buffer is a per-app fixed-size FIFO of slow/error spans. The zero value is
// not usable; construct with NewBuffer.
type Buffer struct {
	mu          sync.Mutex
	size        int
	slowThresh  time.Duration
	byApp       map[string]*ringPerApp
}

type ringPerApp struct {
	items []Span
	head  int // next write index
	full  bool
}

// NewBuffer returns a Buffer that retains up to ringSize entries per app,
// admitting any error span and any span whose duration meets or exceeds
// slowThreshold. A ringSize of 0 returns a no-op Buffer.
func NewBuffer(ringSize int, slowThreshold time.Duration) *Buffer {
	return &Buffer{
		size:       ringSize,
		slowThresh: slowThreshold,
		byApp:      make(map[string]*ringPerApp),
	}
}

// Record admits a span if it meets the retention criteria. Spans with empty
// AppSlug are dropped (they don't belong to a tracked app).
func (b *Buffer) Record(s Span) {
	if b == nil || b.size == 0 || s.AppSlug == "" {
		return
	}
	if s.Status < 500 && time.Duration(s.DurationMS)*time.Millisecond < b.slowThresh && s.Error == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	r, ok := b.byApp[s.AppSlug]
	if !ok {
		r = &ringPerApp{items: make([]Span, b.size)}
		b.byApp[s.AppSlug] = r
	}
	r.items[r.head] = s
	r.head = (r.head + 1) % b.size
	if r.head == 0 {
		r.full = true
	}
}

// Snapshot returns the retained spans for a slug, newest first. Returns nil
// when nothing is buffered for that slug.
func (b *Buffer) Snapshot(slug string) []Span {
	if b == nil || b.size == 0 {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	r, ok := b.byApp[slug]
	if !ok {
		return nil
	}
	var n int
	if r.full {
		n = b.size
	} else {
		n = r.head
	}
	out := make([]Span, 0, n)
	// Iterate newest-first: walk back from (head-1) wrapping around.
	for i := 0; i < n; i++ {
		idx := r.head - 1 - i
		if idx < 0 {
			idx += b.size
		}
		out = append(out, r.items[idx])
	}
	return out
}

// StartProxySpan derives the trace context for one proxy-handled request.
// If the incoming `traceparent` header parses, the returned context continues
// that trace with a fresh span ID; otherwise a new trace is started and the
// sampling decision is made from cfg.SampleRatio. Callers should set
// ret.TraceparentHeader() on the outbound request so downstream services
// (Shiny, FastAPI middleware, etc.) see ShinyHub's span as their parent.
//
// incomingState is the upstream `tracestate` header. Per W3C it is propagated
// downstream unchanged ONLY when the incoming traceparent is valid (the trace
// continues); when a fresh trace is started the stray tracestate is dropped, as
// it references a different trace context.
//
// Returns (context, parentSpanIDHex, sampled). parentSpanIDHex is empty when
// no upstream context was present.
func StartProxySpan(incoming, incomingState string, cfg config.TracingConfig) (TraceContext, string, bool) {
	var (
		traceID  [16]byte
		parentID string
		sampled  bool
		state    string
	)
	if parsed, ok := ParseTraceparent(incoming); ok {
		traceID = parsed.TraceID
		parentID = hex.EncodeToString(parsed.SpanID[:])
		sampled = parsed.Sampled()
		state = strings.TrimSpace(incomingState)
	} else {
		traceID = NewTraceID()
		sampled = SampleByTraceID(traceID, cfg.SampleRatio)
	}
	flags := byte(0)
	if sampled {
		flags = 0x01
	}
	return TraceContext{
		TraceID:    traceID,
		SpanID:     NewSpanID(),
		Flags:      flags,
		TraceState: state,
	}, parentID, sampled
}

// EnvFor returns the OTEL_* environment variables ShinyHub injects into one
// app replica's process. The returned slice is in "KEY=VALUE" form and is
// empty when tracing is disabled or has no endpoint configured.
//
// These are platform DEFAULTS: callers (the process manager) append them
// before the user's per-app env so user-supplied OTEL_* values win on last-
// occurrence-wins semantics. The reserved-prefix rule in the env-var API
// excludes the OTEL_ prefix so users can override per app.
func EnvFor(cfg config.TracingConfig, slug string, replica int) []string {
	if !cfg.Enabled || cfg.OTLPEndpoint == "" {
		return nil
	}
	resource := fmt.Sprintf("shinyhub.app=%s,shinyhub.replica=%d", slug, replica)
	env := []string{
		"OTEL_SERVICE_NAME=" + slug,
		"OTEL_RESOURCE_ATTRIBUTES=" + resource,
		"OTEL_EXPORTER_OTLP_ENDPOINT=" + cfg.OTLPEndpoint,
		"OTEL_EXPORTER_OTLP_PROTOCOL=" + cfg.OTLPProtocol,
		"OTEL_TRACES_SAMPLER=parentbased_traceidratio",
		fmt.Sprintf("OTEL_TRACES_SAMPLER_ARG=%g", cfg.SampleRatio),
	}
	if cfg.OTLPHeaders != "" {
		env = append(env, "OTEL_EXPORTER_OTLP_HEADERS="+cfg.OTLPHeaders)
	}
	return env
}
