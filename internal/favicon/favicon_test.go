package favicon

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEnsurePreservesAuthoredIcon(t *testing.T) {
	page := []byte(`<html><head><LINK href="/mine.png" REL="shortcut ICON"></head><body></body></html>`)
	out, inserted := Ensure(page, AppURL("demo"))
	if inserted || string(out) != string(page) {
		t.Fatal("Ensure replaced an app-authored favicon")
	}
}

func TestEnsureAddsIconBeforeHead(t *testing.T) {
	page := []byte(`<html><HEAD><title>Demo</title></HEAD><body></body></html>`)
	out, inserted := Ensure(page, AppURL("sales & ops"))
	if !inserted {
		t.Fatal("Ensure did not insert a favicon")
	}
	got := string(out)
	if !strings.Contains(got, `<link rel="icon" href="/app/sales%20&amp;%20ops/.shinyhub/favicon">`+"\n</HEAD>") {
		t.Fatalf("unexpected output: %s", got)
	}
}

func TestHasIconDoesNotTreatAppleTouchIconAsFavicon(t *testing.T) {
	if HasIcon([]byte(`<link rel="apple-touch-icon" href="/touch.png">`)) {
		t.Fatal("apple-touch-icon alone must not suppress a browser-tab icon")
	}
}

func TestEnsureTitlePreservesAuthoredTitle(t *testing.T) {
	page := []byte(`<html><head><TITLE>App-owned title</TITLE></head><body></body></html>`)
	out, inserted := EnsureTitle(page, "Revenue · ShinyHub")
	if inserted || string(out) != string(page) {
		t.Fatal("EnsureTitle replaced an app-authored title")
	}
}

func TestEnsureTitlePreservesMalformedAuthoredTitle(t *testing.T) {
	page := []byte(`<html><head><title>App-owned title</head><body></body></html>`)
	out, inserted := EnsureTitle(page, "Revenue · ShinyHub")
	if inserted || string(out) != string(page) {
		t.Fatal("EnsureTitle duplicated an unclosed app-authored title")
	}
}

func TestEnsureTitleAddsEscapedFallback(t *testing.T) {
	page := []byte(`<html><head><!-- <title>not real</title> --></head><body></body></html>`)
	out, inserted := EnsureTitle(page, `Revenue & <Forecast> · ShinyHub`)
	if !inserted {
		t.Fatal("EnsureTitle did not insert a fallback")
	}
	if got := string(out); !strings.Contains(got, `<title>Revenue &amp; &lt;Forecast&gt; · ShinyHub</title>`+"\n</head>") {
		t.Fatalf("unexpected output: %s", got)
	}
}

func TestSetTitleReplacesLifecycleTitle(t *testing.T) {
	page := []byte(`<html><head><title>Starting app…</title></head><body></body></html>`)
	out, changed := SetTitle(page, "Revenue Forecast · ShinyHub")
	if !changed || !strings.Contains(string(out), `<title>Revenue Forecast · ShinyHub</title>`) || strings.Contains(string(out), "Starting app…") {
		t.Fatalf("SetTitle did not replace the lifecycle title: %s", out)
	}
}

func TestEmojiSVGIsEscaped(t *testing.T) {
	got := string(EmojiSVG(`📊</text><script>alert(1)</script>`))
	if strings.Contains(got, "<script>") || !strings.Contains(got, "&lt;/text&gt;") {
		t.Fatalf("EmojiSVG did not escape content: %s", got)
	}
}

func TestWriteRevalidatesAndSupportsHead(t *testing.T) {
	data := EmojiSVG("📊")
	first := httptest.NewRecorder()
	Write(first, httptest.NewRequest(http.MethodGet, "/icon", nil), "image/svg+xml", data)
	if first.Code != http.StatusOK || first.Body.Len() == 0 {
		t.Fatalf("GET = %d/%d bytes", first.Code, first.Body.Len())
	}
	if first.Header().Get("Content-Security-Policy") != "sandbox" {
		t.Fatal("SVG response is not sandboxed")
	}

	head := httptest.NewRecorder()
	Write(head, httptest.NewRequest(http.MethodHead, "/icon", nil), "image/svg+xml", data)
	if head.Code != http.StatusOK || head.Body.Len() != 0 {
		t.Fatalf("HEAD = %d/%d bytes", head.Code, head.Body.Len())
	}

	notModified := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/icon", nil)
	req.Header.Set("If-None-Match", first.Header().Get("ETag"))
	Write(notModified, req, "image/svg+xml", data)
	if notModified.Code != http.StatusNotModified || notModified.Body.Len() != 0 {
		t.Fatalf("conditional GET = %d/%d bytes", notModified.Code, notModified.Body.Len())
	}
}
