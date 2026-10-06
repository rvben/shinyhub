package proxy

import (
	_ "embed"
	"encoding/base64"
	"html"
	"net/http"
)

//go:embed assets/devreload.js
var devReloadScript string

var devReloadCSPHash = "'sha256-" + base64.StdEncoding.EncodeToString(hashOf(devReloadScript)) + "'"

type devReloadSettings struct {
	endpoint string
	revision func() string
}

// SetDevReload enables browser refresh for a local development proxy. revision
// must be safe for concurrent calls and change only after a healthy activation.
// Production proxies leave this disabled. An empty endpoint disables it.
func (p *Proxy) SetDevReload(endpoint string, revision func() string) {
	if endpoint == "" || revision == nil {
		p.devReload.Store(nil)
		return
	}
	p.devReload.Store(&devReloadSettings{endpoint: endpoint, revision: revision})
}

func devReloadPageScript(settings *devReloadSettings) pageScript {
	// Sample before rendering so the document keeps its original revision even
	// when a new activation happens while the response is being assembled.
	revision := settings.revision()
	return pageScript{cspHash: devReloadCSPHash, render: func() string {
		return `<script id="shinyhub-dev-reload" data-url="` + html.EscapeString(settings.endpoint) +
			`" data-revision="` + html.EscapeString(revision) + `">` + devReloadScript + `</script>`
	}}
}

func (p *Proxy) devReloadResponse(resp *http.Response) error {
	if p.devReload.Load() != nil {
		resp.Header.Set("Cache-Control", "no-store")
		resp.Header.Del("ETag")
		resp.Header.Del("Last-Modified")
	}
	return nil
}
