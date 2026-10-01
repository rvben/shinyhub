package workloadmetrics

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/rvben/shinyhub/internal/config"
	collector "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// Export collector-style ResourceMetrics directly: one batched OTLP request can
// contain many workload resources without one SDK provider/reader per process.
type otlpExporter struct {
	url     string
	headers map[string]string
	http    *http.Client
	conn    *grpc.ClientConn
	grpc    collector.MetricsServiceClient
}

type exportError struct {
	reason    string
	retryable bool
	delay     time.Duration
}

func (e *exportError) Error() string { return e.reason }

func grpcFailure(err error) *exportError {
	code := status.Code(err)
	e := &exportError{reason: "OTLP metrics gRPC status " + code.String()}
	for _, detail := range status.Convert(err).Details() {
		if retry, ok := detail.(*errdetails.RetryInfo); ok {
			e.delay = retry.RetryDelay.AsDuration()
			e.retryable = code == codes.ResourceExhausted
		}
	}
	switch code {
	case codes.Canceled, codes.DeadlineExceeded, codes.Aborted, codes.OutOfRange, codes.Unavailable, codes.DataLoss:
		e.retryable = true
	}
	return e
}

func retryAfter(value string) time.Duration {
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds > 0 {
		return time.Duration(min(seconds, int64((1<<63-1)/time.Second))) * time.Second
	}
	if date, err := http.ParseTime(value); err == nil {
		return max(time.Until(date), 0)
	}
	return 0
}

func newExporter(cfg config.TracingConfig) (*otlpExporter, error) {
	e := &otlpExporter{headers: make(map[string]string)}
	for _, pair := range strings.Split(cfg.OTLPHeaders, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if ok && strings.TrimSpace(k) != "" && !strings.EqualFold(strings.TrimSpace(k), "user-agent") {
			e.headers[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	if cfg.OTLPProtocol == "grpc" {
		endpoint := cfg.OTLPEndpoint
		var creds credentials.TransportCredentials = insecure.NewCredentials()
		if u, err := url.Parse(endpoint); err == nil && u.Host != "" {
			endpoint = u.Host
			if u.Scheme == "https" {
				creds = credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12})
			}
		}
		conn, err := grpc.NewClient(endpoint, grpc.WithTransportCredentials(creds), grpc.WithUserAgent("shinyhub-workload-metrics"))
		if err != nil {
			return nil, fmt.Errorf("workload metrics gRPC setup: %w", err)
		}
		e.conn, e.grpc = conn, collector.NewMetricsServiceClient(conn)
	} else {
		u, err := url.Parse(cfg.OTLPEndpoint)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return nil, fmt.Errorf("workload metrics requires an HTTP(S) OTLP endpoint")
		}
		u.Path = strings.TrimRight(u.Path, "/") + "/v1/metrics"
		e.url = u.String()
		// Do not forward authentication headers to a redirect destination.
		e.http = &http.Client{Transport: http.DefaultTransport.(*http.Transport).Clone(), CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	return e, nil
}

func (e *otlpExporter) Export(ctx context.Context, resources []*metricspb.ResourceMetrics) error {
	req := &collector.ExportMetricsServiceRequest{ResourceMetrics: resources}
	var response *collector.ExportMetricsServiceResponse
	if e.grpc != nil {
		md := make(metadata.MD, len(e.headers))
		for k, v := range e.headers {
			md.Set(k, v)
		}
		resp, err := e.grpc.Export(metadata.NewOutgoingContext(ctx, md), req)
		if err != nil {
			return grpcFailure(err)
		}
		response = resp
	} else {
		body, err := proto.Marshal(req)
		if err != nil {
			return &exportError{reason: "OTLP metrics request encoding failed"}
		}
		r, err := http.NewRequestWithContext(ctx, http.MethodPost, e.url, bytes.NewReader(body))
		if err != nil {
			return &exportError{reason: "OTLP metrics request setup failed"}
		}
		for k, v := range e.headers {
			r.Header.Set(k, v)
		}
		r.Header.Set("Content-Type", "application/x-protobuf")
		r.Header.Set("User-Agent", "shinyhub-workload-metrics")
		resp, err := e.http.Do(r)
		if err != nil {
			return &exportError{reason: "OTLP metrics request failed", retryable: true}
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			retry := resp.StatusCode == 429 || resp.StatusCode == 502 || resp.StatusCode == 503 || resp.StatusCode == 504
			return &exportError{reason: fmt.Sprintf("OTLP metrics HTTP status %d", resp.StatusCode), retryable: retry, delay: retryAfter(resp.Header.Get("Retry-After"))}
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, (4<<20)+1))
		if err != nil {
			return &exportError{reason: "OTLP metrics response read failed", retryable: true}
		}
		if len(b) > 4<<20 {
			return &exportError{reason: "OTLP metrics response exceeded 4 MiB"}
		}
		response = new(collector.ExportMetricsServiceResponse)
		if err := proto.Unmarshal(b, response); err != nil {
			return &exportError{reason: "OTLP metrics response decode failed"}
		}
	}
	// Partial-success responses MUST NOT be retried: accepted data may already
	// be stored. Surface the loss without logging backend-controlled contents.
	if p := response.PartialSuccess; p != nil && p.RejectedDataPoints != 0 {
		// Return success to release the completion queue. A diagnostic records
		// rejection count rather than re-sending accepted observations.
		slog.Warn("workload metrics partially rejected", "rejected_data_points", p.RejectedDataPoints)
	}
	return nil
}

func (e *otlpExporter) Close() error {
	if e.conn != nil {
		return e.conn.Close()
	}
	if e.http != nil {
		e.http.CloseIdleConnections()
	}
	return nil
}
