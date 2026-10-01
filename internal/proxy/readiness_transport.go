package proxy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"
)

// ErrReplicaStarting identifies a route recovered from an old server run that
// has not yet proven readiness in this run. No user request has been forwarded.
var ErrReplicaStarting = errors.New("replica has not passed readiness")

type readinessTransport struct {
	base  http.RoundTripper
	check func(context.Context) error
	ready atomic.Bool
	hold  time.Duration
}

// NewReadinessTransport gates an adopted route on readiness, then forwards
// directly after its first successful check. check must bound its context and
// use the original transport. Concurrent requests may probe independently;
// none waits behind another request's health check.
func (p *Proxy) NewReadinessTransport(base http.RoundTripper, check func(context.Context) error) http.RoundTripper {
	if base == nil {
		base = defaultBackendTransport
	}
	return &readinessTransport{base: base, check: check, hold: time.Duration(p.wakeHoldNanos.Load())}
}

func (t *readinessTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !t.ready.Load() {
		ctx := req.Context()
		if t.hold > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, t.hold)
			defer cancel()
		}
		for !t.ready.Load() {
			err := t.check(ctx)
			if err == nil {
				t.ready.Store(true)
				break
			}
			if t.hold <= 0 {
				return nil, fmt.Errorf("%w: %v", ErrReplicaStarting, err)
			}
			select {
			case <-ctx.Done():
				return nil, fmt.Errorf("%w: %v", ErrReplicaStarting, err)
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
	return t.base.RoundTrip(req)
}
