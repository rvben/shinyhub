package proxy

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/appnav"
	"github.com/rvben/shinyhub/internal/favicon"
)

func TestCombinedPageEditsPreserveSequentialBehavior(t *testing.T) {
	for _, page := range []string{
		"<html><head></head><body>hello</body></html>",
		"<HEAD><TITLE>Own</TITLE><LINK REL='shortcut ICON' href='own'></HEAD><BODY>x</BODY>",
		"<head><!-- <title>fake</title><link rel=icon> --></head><body>x</body>",
		"<head><script>var x = '<link rel=icon>';</script></head><body>x</body>",
		"<body>reversed</body><head></head>",
		"<head></head><body>unclosed", "<body>no head</body>", "no tags",
	} {
		t.Run(page, func(t *testing.T) {
			input := []byte(page)
			want, _ := favicon.EnsureTitle(input, "Fallback <title>")
			want, _ = favicon.Ensure(want, "/icon?x=1&y=2")
			want, _ = appnav.SpliceIntoBody(want, "<script>/* helper */</script>")
			got, _ := splicePageMarkup(input, favicon.FallbackMarkup(input, "/icon?x=1&y=2", "Fallback <title>"), "<script>/* helper */</script>")
			if !bytes.Equal(got, want) {
				t.Fatalf("got %s\nwant %s", got, want)
			}
		})
	}
}

type observedPageBody struct {
	io.Reader
	reads  int
	closed bool
}

func (b *observedPageBody) Read(p []byte) (int, error) { b.reads++; return b.Reader.Read(p) }
func (b *observedPageBody) Close() error               { b.closed = true; return nil }

func TestOversizedPageBypassDoesNotConsumeBody(t *testing.T) {
	resp := htmlResponse("demo", testShell)
	body := &observedPageBody{Reader: strings.NewReader(testShell)}
	resp.Body = body
	resp.ContentLength = overlayMaxBodyBytes + 1
	if err := injectPageScripts(overlayOnly("demo"))(resp); err != nil {
		t.Fatal(err)
	}
	if body.reads != 0 || body.closed || resp.Body != body {
		t.Fatal("optional bypass consumed or replaced body")
	}
}

func TestOversizedSupportPageStillRequiresSafeFallback(t *testing.T) {
	resp := htmlResponse("demo", testShell)
	body := &observedPageBody{Reader: strings.NewReader(testShell)}
	resp.Body = body
	resp.ContentLength = overlayMaxBodyBytes + 1
	hook := injectPageScripts(func(*http.Request) []pageScript {
		return []pageScript{{required: true, fallback: "support controls unavailable"}}
	})
	if err := hook(resp); err != nil {
		t.Fatal(err)
	}
	if body.reads != 0 || !body.closed || resp.StatusCode != http.StatusConflict {
		t.Fatalf("reads=%d closed=%t status=%d", body.reads, body.closed, resp.StatusCode)
	}
	if got := readBody(t, resp); got != "support controls unavailable" {
		t.Fatal(got)
	}
}

func TestOptionalScriptsAreOnlyRenderedWhenInjectable(t *testing.T) {
	for _, kind := range []string{"oversized", "CSP", "no-body", "asset"} {
		t.Run(kind, func(t *testing.T) {
			resp := htmlResponse("demo", testShell)
			switch kind {
			case "oversized":
				resp.ContentLength = overlayMaxBodyBytes + 1
			case "CSP":
				resp.Header.Set("Content-Security-Policy", "script-src 'none'")
			case "no-body":
				resp = htmlResponse("demo", "<html><head></head>")
			case "asset":
				resp.Header.Set("Content-Type", "image/png")
			}
			hook := injectPageScripts(func(*http.Request) []pageScript {
				return []pageScript{{render: func() string { t.Error("rendered discarded optional markup"); return "" }, cspHash: overlayCSPHash}}
			})
			if err := hook(resp); err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
		})
	}
}

func TestMetaPolicyNegativeFilterPreservesParserSemantics(t *testing.T) {
	for _, tc := range []struct {
		html   string
		policy bool
	}{
		{`<meta charset="utf-8"><body>hello</body>`, false},
		{`<META HTTP-EQUIV="Content-Security-Policy" content="script-src 'none'">`, true},
		{`<meta http-equiv="content-security-pol&#105;cy" content="script-src 'none'">`, true},
		{`<script>var x = '<meta http-equiv="Content-Security-Policy">';</script>`, false},
		{`<!-- <meta http-equiv="Content-Security-Policy"> -->`, false},
		{`<svg><title><meta http-equiv="Content-Security-Policy"></title></svg>`, true},
	} {
		if got := containsMetaCSP([]byte(tc.html)); got != tc.policy {
			t.Fatalf("policy(%s) = %t", tc.html, got)
		}
	}
}

type pageReadError struct{ err error }

func (r pageReadError) Read(p []byte) (int, error) { return copy(p, "abc"), r.err }

func TestPageReadPreservesDataAndErrors(t *testing.T) {
	for _, length := range []int64{-1, 0, 2, 3, 100} {
		wantErr := errors.New("upstream read failed")
		_, err := readPageBody(pageReadError{wantErr}, length)
		if !errors.Is(err, wantErr) {
			t.Fatalf("length %d: lost read error: %v", length, err)
		}
	}
	for _, size := range []int{0, 3, overlayMaxBodyBytes, overlayMaxBodyBytes + 10} {
		body := strings.Repeat("x", size)
		for _, length := range []int64{-1, 0, 2, int64(size)} {
			got, err := readPageBody(strings.NewReader(body), length)
			if err != nil || string(got) != body[:min(size, overlayMaxBodyBytes+1)] {
				t.Fatalf("size %d, length %d: bytes=%d err=%v", size, length, len(got), err)
			}
		}
	}
}
