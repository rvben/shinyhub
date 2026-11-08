package proxy_test

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/rvben/shinyhub/internal/httpbuf"
	"github.com/rvben/shinyhub/internal/proxy"
)

// cannedTransport answers every forwarded request in memory, so the
// measurements below cover the proxy's own work and nothing of the network.
type cannedTransport struct{}

func (cannedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	const body = "{}"
	return &http.Response{
		StatusCode:    http.StatusOK,
		Header:        http.Header{"Content-Type": {"application/json"}},
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)),
		Request:       r,
	}, nil
}

// discardWriter is a ResponseWriter that keeps nothing, so response bytes
// do not show up in the allocation figures.
type discardWriter struct{ h http.Header }

func (w *discardWriter) Header() http.Header         { return w.h }
func (w *discardWriter) Write(b []byte) (int, error) { return len(b), nil }
func (w *discardWriter) WriteHeader(int)             {}

func newBenchProxy(tb testing.TB, slugs, replicas int) *proxy.Proxy {
	tb.Helper()
	p := proxy.New()
	for i := 0; i < slugs; i++ {
		slug := fmt.Sprintf("app%d", i)
		p.SetPoolSize(slug, replicas)
		for j := 0; j < replicas; j++ {
			if err := p.RegisterReplica(slug, j, fmt.Sprintf("http://127.0.0.1:%d", 20000+j), cannedTransport{}, 0); err != nil {
				tb.Fatal(err)
			}
		}
	}
	return p
}

func serveOnce(p *proxy.Proxy, slug string) {
	req := httptest.NewRequest(http.MethodGet, "/app/"+slug+"/shared/x.js", nil)
	p.ServeHTTP(&discardWriter{h: http.Header{}}, req)
}

// BenchmarkServeHTTP measures one proxied asset request end to end inside
// the process: slug routing, replica selection, the reverse proxy copy and
// the access recorder, against a canned backend.
func BenchmarkServeHTTP(b *testing.B) {
	for _, tc := range []struct {
		name            string
		slugs, replicas int
	}{
		{"1slug_1replica", 1, 1},
		{"50slugs_1replica", 50, 1},
		{"50slugs_4replicas", 50, 4},
	} {
		b.Run(tc.name, func(b *testing.B) {
			p := newBenchProxy(b, tc.slugs, tc.replicas)
			var n atomic.Int64
			b.ReportAllocs()
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				slug := fmt.Sprintf("app%d", int(n.Add(1))%tc.slugs)
				for pb.Next() {
					serveOnce(p, slug)
				}
			})
		})
	}
}

// A proxied response must not allocate its own copy buffer: without a
// BufferPool, httputil.ReverseProxy allocates 32 KiB per response, which was
// about 90% of the bytes the data plane allocated per request.
func TestServeHTTP_RecyclesCopyBuffer(t *testing.T) {
	p := newBenchProxy(t, 1, 1)
	serveOnce(p, "app0")
	result := testing.Benchmark(func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			serveOnce(p, "app0")
		}
	})
	if got := result.AllocedBytesPerOp(); got >= httpbuf.Size {
		t.Fatalf("a proxied request allocates %d B/op, at least one full %d B copy buffer; the reverse proxy is not drawing from the shared pool", got, httpbuf.Size)
	}
}
