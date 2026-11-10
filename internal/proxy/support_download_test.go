package proxy

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/auth"
)

func supportDownloadResponse(t *testing.T, contentType, disposition string, body io.ReadCloser) *http.Response {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/app/sales/session/id/download/dl_csv?w=", nil)
	r.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	r.Header.Set("Sec-Fetch-Dest", "document")
	r.Header.Set("Sec-Fetch-Mode", "navigate")
	r = r.WithContext(auth.WithUser(r.Context(), &auth.ContextUser{
		ID: 22, Username: "subject", Role: "viewer", SupportSession: &auth.SupportSessionContext{
			ID: "test", ActorID: 11, ActorUsername: "actor", AppID: 10, AppSlug: "sales", ExpiresAt: time.Now().Add(time.Hour),
		},
	}))
	resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: body, ContentLength: -1, Request: r}
	if contentType != "" {
		resp.Header.Set("Content-Type", contentType)
	}
	if disposition != "" {
		resp.Header.Set("Content-Disposition", disposition)
	}
	return resp
}

func TestAttachmentDispositionIsUnambiguous(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  bool
	}{
		{"attachment", true},
		{`ATTACHMENT; filename*=UTF-8''%e2%82%ac.csv`, true},
		{`attachment; filename="a;b.csv"`, true},
		{`"attachment"`, false},
		{`attachment (comment)`, false},
		{`inline, attachment`, false},
		{`attachment, inline`, false},
		{`attachment; filename="a"; filename="b"`, false},
		{`attachment; filename="broken`, false},
		{`attachment; filename=Q3 report.csv`, false},
	} {
		t.Run(tc.value, func(t *testing.T) {
			resp := supportDownloadResponse(t, "text/html", tc.value, io.NopCloser(strings.NewReader(testShell)))
			if got := attachmentDownload(resp); got != tc.want {
				t.Fatalf("attachment=%t want=%t", got, tc.want)
			}
			if tc.want && injectableResponse(resp) {
				t.Fatal("attachment classified as injectable HTML")
			}
		})
	}
}

func TestSupportSessionDownloadThroughProxy(t *testing.T) {
	for _, contentType := range []string{"text/csv", "text/html"} {
		t.Run(contentType, func(t *testing.T) {
			const payload = "<html><body>original export</body></html>"
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", contentType)
				w.Header().Set("Content-Disposition", `attachment; filename="report"`)
				w.Header().Set("ETag", `"export"`)
				_, _ = io.WriteString(w, payload)
			}))
			defer backend.Close()
			p := New()
			p.SetSupportSessions(true)
			p.SetPoolSize("sales", 1)
			p.SetPoolAppID("sales", 10)
			if err := p.RegisterReplica("sales", 0, backend.URL, nil, 1); err != nil {
				t.Fatal(err)
			}
			req := supportDownloadResponse(t, contentType, "attachment", nil).Request
			rec := httptest.NewRecorder()
			p.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK || rec.Body.String() != payload || rec.Header().Get("ETag") != `"export"` {
				t.Fatalf("status=%d headers=%v body=%s", rec.Code, rec.Header(), rec.Body.String())
			}
		})
	}
}

func TestSupportSessionDownloadsStreamUnchanged(t *testing.T) {
	for _, contentType := range []string{"text/csv; charset=utf-8", "text/plain", "application/octet-stream", "application/pdf", "application/zip", "application/json", "text/html", "application/xhtml+xml", "image/svg+xml", "application/xml", ""} {
		for _, status := range []int{http.StatusOK, http.StatusPartialContent} {
			t.Run(contentType+http.StatusText(status), func(t *testing.T) {
				// A compressed HTML-looking payload must remain byte-for-byte intact;
				// the attachment disposition prevents document rendering.
				var compressed bytes.Buffer
				zw := gzip.NewWriter(&compressed)
				if _, err := io.WriteString(zw, "<html><body>export,with,data</body></html>"); err != nil {
					t.Fatal(err)
				}
				if err := zw.Close(); err != nil {
					t.Fatal(err)
				}
				body := &observedPageBody{Reader: bytes.NewReader(compressed.Bytes())}
				resp := supportDownloadResponse(t, contentType, `Attachment; filename="export.csv"`, body)
				resp.StatusCode = status
				resp.ContentLength = int64(compressed.Len())
				resp.Header.Set("Content-Encoding", "gzip")
				resp.Header.Set("Content-Length", strconv.Itoa(compressed.Len()))
				resp.Header.Set("ETag", `"original"`)
				resp.Header.Set("Accept-Ranges", "bytes")
				resp.Header.Set("Content-Digest", "original-digest")
				if status == http.StatusPartialContent {
					resp.Header.Set("Content-Range", "bytes 0-"+strconv.Itoa(compressed.Len()-1)+"/"+strconv.Itoa(compressed.Len()))
				}
				resp.Header.Set("Content-Security-Policy", "script-src 'none'")
				p := New()
				p.SetSupportSessions(true)
				if err := p.modifyResponseFor("sales")(resp); err != nil {
					t.Fatal(err)
				}
				if resp.StatusCode != status || resp.Body != body || body.reads != 0 || body.closed {
					t.Fatalf("download consumed/replaced: status=%d reads=%d closed=%t", resp.StatusCode, body.reads, body.closed)
				}
				for header, want := range map[string]string{"Content-Encoding": "gzip", "Content-Length": strconv.Itoa(compressed.Len()), "ETag": `"original"`, "Accept-Ranges": "bytes", "Content-Digest": "original-digest", "Content-Disposition": `Attachment; filename="export.csv"`, "Content-Security-Policy": "script-src 'none'"} {
					if got := resp.Header.Get(header); got != want {
						t.Errorf("%s=%q want=%q", header, got, want)
					}
				}
				got, err := io.ReadAll(resp.Body)
				if err != nil || !bytes.Equal(got, compressed.Bytes()) {
					t.Fatalf("body changed: %v", err)
				}
			})
		}
	}
}

func TestSupportSessionAttachmentCannotBypassPageChecks(t *testing.T) {
	for _, tc := range []struct {
		name, contentType, disposition string
		mutate                         func(*http.Response)
	}{
		{name: "inline CSV", contentType: "text/csv", disposition: "inline"},
		{name: "no disposition", contentType: "text/csv"},
		{name: "malformed disposition", contentType: "text/csv", disposition: `attachment; filename="broken`},
		{name: "HTML", contentType: "text/html", disposition: "inline"},
		{name: "XHTML", contentType: "application/xhtml+xml", disposition: "inline"},
		{name: "SVG", contentType: "image/svg+xml", disposition: "inline"},
		{name: "XML", contentType: "application/xml", disposition: "inline"},
		{name: "other XML", contentType: "application/example+xml", disposition: "inline"},
		{name: "duplicate disposition", contentType: "text/csv", disposition: "attachment", mutate: func(r *http.Response) { r.Header.Add("Content-Disposition", "inline") }},
		{name: "error response", contentType: "text/csv", disposition: "attachment", mutate: func(r *http.Response) { r.StatusCode = http.StatusForbidden }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := supportDownloadResponse(t, tc.contentType, tc.disposition, io.NopCloser(strings.NewReader("<html><body>private</body></html>")))
			resp.Header.Set("Content-Security-Policy", "script-src 'none'")
			resp.Header.Set("Content-Range", "bytes 0-3/10")
			resp.Header.Set("Digest", "sha-256=discarded")
			resp.Trailer = http.Header{"Content-Digest": []string{"discarded"}}
			if tc.mutate != nil {
				tc.mutate(resp)
			}
			p := New()
			p.SetSupportSessions(true)
			if err := p.modifyResponseFor("sales")(resp); err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != http.StatusConflict || !strings.Contains(string(body), "Support session paused") {
				t.Fatalf("status=%d body=%s", resp.StatusCode, body)
			}
			for _, header := range []string{"Content-Disposition", "Content-Range", "Digest"} {
				if got := resp.Header.Get(header); got != "" {
					t.Errorf("discarded %s survived: %s", header, got)
				}
			}
			if resp.Trailer != nil {
				t.Fatal("discarded trailers survived")
			}
		})
	}
}

func TestSupportSessionHTMLDownloadDoesNotEmbedSupportMetadata(t *testing.T) {
	resp := supportDownloadResponse(t, "text/html", `attachment; filename="report.html"`, io.NopCloser(strings.NewReader(testShell)))
	p := New()
	p.SetSupportSessions(true)
	if err := p.modifyResponseFor("sales")(resp); err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || string(body) != testShell {
		t.Fatalf("status=%d changed exported HTML", resp.StatusCode)
	}
	if resp.Header.Get("Content-Disposition") != `attachment; filename="report.html"` {
		t.Fatal("HTML disposition changed")
	}
}

func TestHTMLAttachmentBypassesAllPageEnhancementsWithoutBuffering(t *testing.T) {
	for _, length := range []int64{-1, overlayMaxBodyBytes + 1} {
		resp := htmlResponse("sales", testShell)
		body := &observedPageBody{Reader: strings.NewReader(testShell)}
		resp.Body = body
		resp.ContentLength = length
		resp.Header.Set("Content-Disposition", `attachment; filename="report.html"`)
		p := New()
		p.SetStatusOverlay(true)
		p.SetAppFavicon(true)
		p.SetAppNav(true, "https://hub.example.com/")
		if err := p.modifyResponseFor("sales")(resp); err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK || resp.Body != body || body.reads != 0 || body.closed || resp.ContentLength != length {
			t.Fatalf("attachment modified: status=%d reads=%d closed=%t length=%d", resp.StatusCode, body.reads, body.closed, resp.ContentLength)
		}
	}
}
