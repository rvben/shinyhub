package proxy

import (
	"log/slog"
	"net/http"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
)

// renderNoopTransport is a minimal transport stub for reconcileSlug; it mirrors
// poolsync_test.go's noopTransport (a no-op) so NewPoolSyncer accepts it. It is
// redefined here because that stub lives in the external poolsync_test.go
// package and is not importable from this internal-package test file.
type renderNoopTransport struct{}

func (renderNoopTransport) TransportForReplica(_ *db.Replica) (http.RoundTripper, error) {
	return nil, nil
}

// TestPoolSync_AppliesRenderPacing asserts that reconcileSlug reads the app's
// render_seconds from the joined row and applies it to the live limiter, so a
// change to render pacing converges on the next reconcile pass (across
// instances, and on recovery) instead of only taking effect at deploy time.
func TestPoolSync_AppliesRenderPacing(t *testing.T) {
	prx := New()
	prx.SetRenderLimiterFactory(testFactory())
	syncer := NewPoolSyncer(prx, nil, renderNoopTransport{}, slog.Default(), false)

	rows := []db.RoutableReplica{{
		Slug:             "demo",
		AppRenderSeconds: 1.3,
		Replica:          &db.Replica{AppID: 1, Index: 0, Status: "running"},
	}}
	syncer.reconcileSlug("demo", rows)

	if prx.appLimiter("demo") == nil {
		t.Fatal("reconcileSlug with render_seconds > 0 should install a limiter")
	}
}
