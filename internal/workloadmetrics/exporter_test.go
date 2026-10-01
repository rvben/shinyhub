package workloadmetrics

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/process"
	collector "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestHTTPExportBatchesResourcesAndCarriesConfiguredHeaders(t *testing.T) {
	e, err := newExporter(config.TracingConfig{OTLPEndpoint: "https://collector/prefix/", OTLPProtocol: "http/protobuf", OTLPHeaders: "x-token=secret,user-agent=personal-metadata"})
	if err != nil {
		t.Fatal(err)
	}
	e.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://collector/prefix/v1/metrics" || r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/x-protobuf" || r.Header.Get("User-Agent") != "shinyhub-workload-metrics" || r.Header.Get("x-token") != "secret" {
			t.Fatalf("request = %v", r)
		}
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		var req collector.ExportMetricsServiceRequest
		if err := proto.Unmarshal(b, &req); err != nil {
			t.Fatal(err)
		}
		if len(req.ResourceMetrics) != 2 {
			t.Fatalf("resources = %d", len(req.ResourceMetrics))
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(nil))}, nil
	})
	if err := e.Export(context.Background(), []*metricspb.ResourceMetrics{{}, {}}); err != nil {
		t.Fatal(err)
	}
}

func TestHTTPRetryClassificationAndPartialSuccess(t *testing.T) {
	for _, tc := range []struct {
		code  int
		retry bool
	}{{400, false}, {401, false}, {500, false}, {429, true}, {502, true}, {503, true}, {504, true}, {302, false}} {
		t.Run(http.StatusText(tc.code), func(t *testing.T) {
			e, err := newExporter(config.TracingConfig{OTLPEndpoint: "http://collector:4318"})
			if err != nil {
				t.Fatal(err)
			}
			e.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tc.code, Header: http.Header{"Retry-After": {"120"}}, Body: io.NopCloser(strings.NewReader("backend-controlled-details"))}, nil
			})
			err = e.Export(context.Background(), nil)
			var failure *exportError
			if !errors.As(err, &failure) || failure.retryable != tc.retry || failure.delay != 120*time.Second || strings.Contains(err.Error(), "backend-controlled") {
				t.Fatalf("failure = %v", err)
			}
		})
	}
	e, _ := newExporter(config.TracingConfig{OTLPEndpoint: "http://collector:4318"})
	e.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		b, _ := proto.Marshal(&collector.ExportMetricsServiceResponse{PartialSuccess: &collector.ExportMetricsPartialSuccess{RejectedDataPoints: 1, ErrorMessage: "private-backend-details"}})
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(b))}, nil
	})
	if err := e.Export(context.Background(), nil); err != nil {
		t.Fatalf("partial success must not retry: %v", err)
	}
}

func TestPermanentFailureDropsCompletedObservations(t *testing.T) {
	c, _ := testCollector()
	e, _ := newExporter(config.TracingConfig{OTLPEndpoint: "http://collector:4318"})
	e.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 400, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	c.exporter = e
	c.Observe(process.StartParams{Slug: "app", JobRunID: 1}, process.RunHandle{PID: 10}, "")()
	if err := c.export(context.Background(), c.collect()); err == nil {
		t.Fatal("want permanent failure")
	}
	if len(c.completed) != 0 {
		t.Fatal("permanently rejected observations would be retried")
	}
}

type grpcMetricsServer struct {
	collector.UnimplementedMetricsServiceServer
	t *testing.T
}

func (s grpcMetricsServer) Export(ctx context.Context, req *collector.ExportMetricsServiceRequest) (*collector.ExportMetricsServiceResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	if md.Get("x-token")[0] != "secret" || !strings.HasPrefix(md.Get("user-agent")[0], "shinyhub-workload-metrics") || len(req.ResourceMetrics) != 2 {
		s.t.Errorf("bad gRPC request or metadata: %v", md)
	}
	return &collector.ExportMetricsServiceResponse{}, nil
}

func TestGRPCExport(t *testing.T) {
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	collector.RegisterMetricsServiceServer(server, grpcMetricsServer{t: t})
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })
	e, err := newExporter(config.TracingConfig{OTLPEndpoint: "http://collector:4317", OTLPProtocol: "grpc", OTLPHeaders: "x-token=secret,user-agent=personal-metadata"})
	if err != nil {
		t.Fatal(err)
	}
	_ = e.Close()
	conn, err := grpc.NewClient("passthrough:///memory", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithUserAgent("shinyhub-workload-metrics"), grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }))
	if err != nil {
		t.Fatal(err)
	}
	e.conn, e.grpc = conn, collector.NewMetricsServiceClient(conn)
	defer e.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := e.Export(ctx, []*metricspb.ResourceMetrics{{}, {}}); err != nil {
		t.Fatal(err)
	}
}

func TestGRPCRetryClassification(t *testing.T) {
	for _, tc := range []struct {
		code  codes.Code
		retry bool
	}{{codes.InvalidArgument, false}, {codes.Unavailable, true}, {codes.ResourceExhausted, false}, {codes.Internal, false}, {codes.DeadlineExceeded, true}} {
		if got := grpcFailure(status.Error(tc.code, "private-details")); got.retryable != tc.retry || strings.Contains(got.Error(), "private-details") {
			t.Fatalf("classification = %v", got)
		}
	}
	st, err := status.New(codes.ResourceExhausted, "throttled").WithDetails(&errdetails.RetryInfo{RetryDelay: durationpb.New(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if got := grpcFailure(st.Err()); !got.retryable || got.delay != time.Minute {
		t.Fatalf("throttling = %v", got)
	}
}

func TestNewRejectsUnconfiguredExport(t *testing.T) {
	if _, err := New(config.TracingConfig{}, 30*time.Second, "test"); err == nil {
		t.Fatal("unconfigured OTLP should be rejected before creating a collector")
	}
}

func TestReplicaCountResourceStableAcrossExporterRestarts(t *testing.T) {
	var countResources []*metricspb.ResourceMetrics
	var launchIDs []string
	for i := 0; i < 2; i++ {
		c, err := New(config.TracingConfig{Enabled: true, OTLPEndpoint: "http://collector:4318"}, time.Second, "127.0.0.1:8080")
		if err != nil {
			t.Fatal(err)
		}
		defer c.exporter.Close()
		fake, _ := testCollector()
		c.sampler, c.now = fake.sampler, fake.now
		end := c.Observe(process.StartParams{Slug: "app"}, process.RunHandle{PID: 10}, "")
		for _, rm := range c.collect().resources {
			if rm.ScopeMetrics[0].Metrics[0].Name == "shinyhub.replicas" {
				countResources = append(countResources, rm)
				continue
			}
			for _, kv := range rm.Resource.Attributes {
				if kv.Key == "service.instance.id" {
					launchIDs = append(launchIDs, kv.Value.GetStringValue())
				}
			}
		}
		end()
	}
	if len(countResources) != 2 || !proto.Equal(countResources[0].Resource, countResources[1].Resource) {
		t.Fatal("restart creates a duplicate replica-count series")
	}
	if len(launchIDs) != 2 || launchIDs[0] == launchIDs[1] {
		t.Fatal("restart mixed cumulative workload streams")
	}
	other, err := New(config.TracingConfig{Enabled: true, OTLPEndpoint: "http://collector:4318"}, time.Second, "127.0.0.1:8081")
	if err != nil {
		t.Fatal(err)
	}
	defer other.exporter.Close()
	for _, attr := range countResources[0].Resource.Attributes {
		if attr.Key == "service.instance.id" && attr.Value.GetStringValue() == other.base["service.instance.id"] {
			t.Fatal("independent server listeners share a replica-count identity")
		}
	}
}
