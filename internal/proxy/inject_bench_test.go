package proxy

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// BenchmarkHTMLDecoration isolates response processing from app rendering and network latency.
func BenchmarkHTMLDecoration(b *testing.B) {
	for _, size := range []int{32 << 10, 1 << 20, 3 << 20} {
		for _, enabled := range []bool{false, true} {
			b.Run(fmt.Sprintf("bytes-%d/enabled-%t", size, enabled), func(b *testing.B) {
				benchmarkHTMLDecoration(b, size, enabled, "")
			})
		}
	}
}

// Exercise common metadata and stylesheet tags as well as the minimal shell.
func BenchmarkHTMLDecorationStyled(b *testing.B) {
	benchmarkHTMLDecoration(b, 1<<20, true, `<meta charset="utf-8"><meta name="viewport" content="width=device-width"><link rel="stylesheet" href="app.css">`)
}

func benchmarkHTMLDecoration(b *testing.B, size int, enabled bool, head string) {
	p := New()
	p.SetStatusOverlay(enabled)
	p.SetAppFavicon(enabled)
	p.SetAppNav(enabled, "/")
	hook := p.modifyResponseFor("bench")
	body := "<!doctype html><html><head><title>Bench</title>" + head + "</head><body>" + strings.Repeat("x", size) + "</body></html>"
	req := httptest.NewRequest("GET", "/app/bench/", nil)
	req.Header.Set("Accept", "text/html")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/html"}}, Body: io.NopCloser(strings.NewReader(body)), Request: req, ContentLength: int64(len(body))}
		if err := hook(resp); err != nil {
			b.Fatal(err)
		}
		if enabled && size < overlayMaxBodyBytes && resp.ContentLength <= int64(len(body)) {
			b.Fatal("decoration missing")
		}
		if _, err := io.Copy(io.Discard, resp.Body); err != nil {
			b.Fatal(err)
		}
		resp.Body.Close()
	}
}
