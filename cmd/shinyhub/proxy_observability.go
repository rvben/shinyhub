package main

import (
	"errors"
	"log/slog"

	"github.com/rvben/shinyhub/internal/api"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/metrics"
	"github.com/rvben/shinyhub/internal/proxy"
)

// configureProxyObservability wires what the proxy reports about the traffic
// it carries: the per-request access log, the unknown-slug lookup behind its
// 404s, and the WebSocket session-end log and counters. metricsReg is nil when
// metrics are disabled.
func configureProxyObservability(logger *slog.Logger, prx *proxy.Proxy, srv *api.Server, store *db.Store, metricsReg *metrics.Registry) {
	// The Server's trusted-proxy-aware ClientIP keeps the access log's
	// "client" field honest when shinyhub itself sits behind an edge proxy;
	// this is independent of anything the backend app (uvicorn/httpuv)
	// chooses to print in its own log and gives operators a reliable
	// per-slug audit trail.
	prx.SetClientIPResolver(srv.ClientIP)
	prx.SetSlugExists(proxySlugExists(store, logger))
	prx.SetAccessLogger(proxyAccessLogger(logger))
	prx.SetWSSessionEndRecorder(wsSessionEndRecorder(logger, metricsReg, proxy.NewWSAbnormalBurstDetector()))
}

// proxySlugExists distinguishes unknown-slug requests from hibernated-app
// requests, so the proxy returns 404 for typos and deleted apps instead of
// looping the loading page indefinitely. It runs only on a pool miss.
//
// A missing row (false, nil) and a failed lookup (false, err) are different
// facts: the proxy shows the loading page on an error, because reporting a
// real app as deleted whenever the database hiccups would mask a transient
// infrastructure issue as a permanent one.
func proxySlugExists(store *db.Store, logger *slog.Logger) func(slug string) (bool, error) {
	return func(slug string) (bool, error) {
		_, err := store.GetAppBySlug(slug)
		if err == nil {
			return true, nil
		}
		if errors.Is(err, db.ErrNotFound) {
			return false, nil
		}
		logger.Warn("proxy_slug_lookup_failed", "slug", slug, "error", err.Error())
		return false, err
	}
}

// proxyAccessLogger emits one structured record per proxied app request.
// Replica, rejection and fallback detail appear only when they apply.
func proxyAccessLogger(logger *slog.Logger) func(proxy.AccessLogEntry) {
	return func(e proxy.AccessLogEntry) {
		attrs := []any{
			"slug", e.Slug,
			"method", e.Method,
			"path", e.Path,
			"status", e.Status,
			"bytes", e.Bytes,
			"duration_ms", e.Duration.Milliseconds(),
			"client_ip", e.ClientIP,
			"peer", e.Peer,
		}
		if e.ReplicaIndex >= 0 {
			attrs = append(attrs, "replica", e.ReplicaIndex, "sticky", e.Sticky)
		}
		if e.Reject != "" {
			attrs = append(attrs, "reject", string(e.Reject))
		}
		if e.Fallback {
			attrs = append(attrs, "fallback", true, "fallback_reason", e.FallbackReason)
		}
		logger.Info("proxy_access", attrs...)
	}
}

// wsSessionEndRecorder logs each finished WebSocket tunnel, at WARN when it
// ended abnormally, counts it when metrics are enabled, and reports a burst
// when abnormal endings cluster on one replica generation.
func wsSessionEndRecorder(logger *slog.Logger, metricsReg *metrics.Registry, bursts *proxy.WSAbnormalBurstDetector) func(proxy.WSSessionEnd) {
	return func(e proxy.WSSessionEnd) {
		attrs := []any{
			"slug", e.Slug, "connection_id", e.ConnectionID, "replica", e.ReplicaIndex,
			"deployment_id", e.DeploymentID, "duration_ms", e.Duration.Milliseconds(),
			"closed_by", e.ClosedBy, "end_signal", e.EndSignal,
			"transport_end_side", e.TransportEndSide, "bytes_to_client", e.BytesToClient,
			"bytes_to_upstream", e.BytesToUpstream, "abnormal", e.Abnormal,
		}
		if e.CloseCode != nil {
			attrs = append(attrs, "close_code", *e.CloseCode)
		}
		if e.CloseReason != "" {
			attrs = append(attrs, "close_reason", e.CloseReason)
		}
		if metricsReg != nil {
			metricsReg.RecordWSSessionEnd(e.Slug, e.ClosedBy, e.TransportEndSide, e.Abnormal, e.Duration)
		}
		if e.Abnormal {
			logger.Warn("ws_session_end", attrs...)
		} else {
			logger.Info("ws_session_end", attrs...)
		}
		if burst, ok := bursts.Record(e); ok {
			if metricsReg != nil {
				metricsReg.RecordWSAbnormalBurst(burst.Slug)
			}
			burstAttrs := []any{
				"slug", burst.Slug, "replica", burst.Replica,
				"deployment_id", burst.DeploymentID, "count", burst.Count,
				"window_ms", burst.Window.Milliseconds(), "span_ms", burst.Span.Milliseconds(),
			}
			if burst.CloseCode != nil {
				burstAttrs = append(burstAttrs, "close_code", *burst.CloseCode)
			}
			if burst.CloseReason != "" {
				burstAttrs = append(burstAttrs, "close_reason", burst.CloseReason)
			}
			logger.Warn("ws_abnormal_burst", burstAttrs...)
		}
	}
}
