package proxy

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestDevReloadIsOptInAndCSPBound(t *testing.T) {
	p := New()
	body := "<html><body>App</body></html>"
	response := htmlResponse("demo", body)
	if err := p.modifyResponseFor("demo")(response); err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(response.Body)
	if strings.Contains(string(b), "shinyhub-dev-reload") {
		t.Fatal("production proxy injected dev refresh")
	}

	revision := "session:1"
	p.SetDevReload("/app/demo/__shinyhub_dev_revision", func() string { return revision })
	response = htmlResponse("demo", body)
	response.Header.Set("Content-Security-Policy", "script-src 'self'; connect-src 'self'")
	response.Header.Set("Cache-Control", "public, max-age=3600")
	response.Header.Set("ETag", "old")
	if err := p.modifyResponseFor("demo")(response); err != nil {
		t.Fatal(err)
	}
	b, _ = io.ReadAll(response.Body)
	if !strings.Contains(string(b), `data-revision="session:1"`) || !strings.Contains(response.Header.Get("Content-Security-Policy"), devReloadCSPHash) {
		t.Fatalf("missing refresh or CSP hash: %s / %s", b, response.Header.Get("Content-Security-Policy"))
	}
	if response.Header.Get("Cache-Control") != "no-store" || response.Header.Get("ETag") != "" {
		t.Fatal("dev HTML can be cached")
	}
	revision = "session:2"
	scripts := p.pageScriptsFor(appPageLoad("demo"), "demo", 0)
	if !strings.Contains(scripts[0].render(), `data-revision="session:2"`) {
		t.Fatal("new page got stale revision")
	}
	p.SetDevReload("", nil)
	if p.injectsPageHTML() {
		t.Fatal("disable left refresh enabled")
	}
}

func TestDevReloadBypassesConditionalAssetCache(t *testing.T) {
	p := New()
	p.SetDevReload("/revision", func() string { return "1" })
	req, _ := http.NewRequest("GET", "http://localhost/app/demo/style.css", nil)
	req.Header.Set("If-None-Match", "old")
	req.Header.Set("If-Modified-Since", "yesterday")
	p.relaxEncodingForInjection(req)
	if req.Header.Get("If-None-Match") != "" || req.Header.Get("If-Modified-Since") != "" {
		t.Fatal("conditional asset request was preserved")
	}
}
