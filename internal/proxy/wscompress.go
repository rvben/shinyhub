package proxy

import (
	"io"
	"net/http"
	"strings"

	"github.com/rvben/shinyhub/internal/wsdeflate"
)

// compressWebSocket gives a WebSocket permessage-deflate when the client
// offers it and the backend answered the upgrade without negotiating any
// extension, which is how R Shiny's httpuv always answers. The proxy then
// accepts the offer itself and translates frames between the compressed
// client link and the plain backend link.
//
// A backend that negotiated an extension of its own (uvicorn negotiates
// permessage-deflate for Python Shiny) is left alone: the two ends already
// agree, and the proxy relays their bytes untouched.
func (p *Proxy) compressWebSocket(resp *http.Response) error {
	if resp.StatusCode != http.StatusSwitchingProtocols || !p.wsCompression.Load() {
		return nil
	}
	if !strings.EqualFold(strings.TrimSpace(resp.Header.Get("Upgrade")), "websocket") {
		return nil
	}
	if len(resp.Header.Values("Sec-WebSocket-Extensions")) > 0 || resp.Request == nil {
		return nil
	}
	backend, ok := resp.Body.(io.ReadWriteCloser)
	if !ok {
		return nil
	}
	accept, ok := wsdeflate.Negotiate(resp.Request.Header.Values("Sec-WebSocket-Extensions"))
	if !ok {
		return nil
	}
	resp.Header.Set("Sec-WebSocket-Extensions", accept)
	resp.Body = wsdeflate.NewTranslator(backend)
	return nil
}
