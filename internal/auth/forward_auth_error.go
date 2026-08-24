package auth

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"strings"

	"github.com/rvben/shinyhub/internal/favicon"
)

const forwardAuthProxyErrorCSS = `:root{color-scheme:dark light;--canvas:#060914;--surface:#141b32;--text:#e8eeff;--soft:#a8b4d4;--muted:#7f8db3;--accent:#38bdf8;--warning:#fbbf24;--code:#0e1426}*{box-sizing:border-box}body{min-height:100svh;margin:0;padding:24px;display:grid;place-items:center;background:var(--canvas);color:var(--text);font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif}.page{width:min(100%,680px)}.brand{display:flex;align-items:center;gap:10px;margin-bottom:28px;font-size:1rem;font-weight:700;letter-spacing:-.02em}.mark{width:11px;height:11px;background:var(--accent);transform:rotate(45deg);border-radius:2px;box-shadow:0 0 0 4px color-mix(in srgb,var(--accent) 15%,transparent)}.status{display:inline-flex;align-items:center;gap:8px;margin:0 0 14px;color:var(--warning);font-size:.78rem;font-weight:700}.status-dot{width:7px;height:7px;border-radius:50%;background:currentColor}h1{max-width:18ch;margin:0;font-size:3rem;font-weight:300;line-height:1.05;letter-spacing:-.035em;text-wrap:balance}.lead{max-width:62ch;margin:18px 0 30px;color:var(--soft);font-size:1rem;line-height:1.65;text-wrap:pretty}.operator{padding:22px;background:var(--surface);border-radius:14px}.operator h2{margin:0 0 8px;font-size:1rem;letter-spacing:-.01em}.operator p{margin:0;color:var(--soft);font-size:.9rem;line-height:1.6}.operator code,.snippet{font-family:"SFMono-Regular",Consolas,monospace}.operator code{color:var(--text)}.snippet-label{display:block;margin:18px 0 7px;color:var(--muted);font-size:.75rem;font-weight:600}.snippet{margin:0;padding:13px 14px;overflow-x:auto;background:var(--code);color:var(--text);border-radius:8px;font-size:.8rem;line-height:1.55}.note{margin-top:14px!important;color:var(--muted)!important;font-size:.8rem!important}.actions{display:flex;align-items:center;gap:16px;margin-top:22px}.actions button{min-height:40px;padding:9px 18px;border:0;border-radius:8px;background:var(--accent);color:#030510;font:700 .8rem/1 -apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;cursor:pointer}.actions button:focus-visible{outline:3px solid color-mix(in srgb,var(--accent) 45%,transparent);outline-offset:3px}.http-status{color:var(--muted);font:400 .76rem/1.4 "SFMono-Regular",Consolas,monospace}@media(prefers-color-scheme:light){:root{--canvas:#f4f7fc;--surface:#fff;--text:#16203a;--soft:#45526e;--muted:#5b6784;--accent:#0369a1;--warning:#b45309;--code:#e8edf6}.actions button{color:#fff}}@media(max-width:520px){body{place-items:start center;padding:28px 18px}.brand{margin-bottom:24px}h1{font-size:2.25rem}.operator{padding:18px}.actions{align-items:flex-start;flex-direction:column;gap:12px}}`

// writeForwardAuthProxyError turns a reverse-proxy wiring mistake into a clear
// deployment state. A browser navigation gets an operator-actionable page;
// API and CLI callers retain a machine-readable response. The configured
// secret is never included in either response.
func writeForwardAuthProxyError(w http.ResponseWriter, r *http.Request, secretHeader string, missing bool) {
	code := "forward_auth_secret_mismatch"
	detail := "The reverse proxy sent a credential that does not match auth.forward_auth.shared_secret."
	lead := "ShinyHub received your signed-in identity, but the reverse proxy sent a credential that ShinyHub could not verify."
	if missing {
		code = "forward_auth_secret_header_missing"
		detail = fmt.Sprintf("The reverse proxy did not send the required %s header.", secretHeader)
		lead = "ShinyHub received your signed-in identity, but the reverse proxy did not include the credential ShinyHub uses to trust it."
	}

	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Robots-Tag", "noindex")
	if !requestWantsHTML(r) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error":   "forward auth proxy configuration error",
			"code":    code,
			"message": detail,
		})
		return
	}

	styleHash := sha256.Sum256([]byte(forwardAuthProxyErrorCSS))
	w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src 'self'; style-src 'sha256-"+base64.StdEncoding.EncodeToString(styleHash[:])+"'; form-action 'self'; base-uri 'none'; frame-ancestors 'self'")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusServiceUnavailable)

	header := html.EscapeString(secretHeader)
	_, _ = fmt.Fprintf(w, `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Reverse proxy setup incomplete · ShinyHub</title>
%s
<style>%s</style>
</head>
<body>
<main class="page">
  <div class="brand"><span class="mark" aria-hidden="true"></span><span>ShinyHub</span></div>
  <p class="status"><span class="status-dot" aria-hidden="true"></span>Configuration required</p>
  <h1>Reverse proxy setup incomplete</h1>
  <p class="lead">%s</p>
  <section class="operator" aria-labelledby="operator-heading">
    <h2 id="operator-heading">For the operator</h2>
    <p>Configure the reverse proxy to overwrite <code>%s</code> on every request, using the same value as <code>auth.forward_auth.shared_secret</code>.</p>
    <span class="snippet-label">Caddy · inside the reverse_proxy block</span>
    <pre class="snippet"><code>header_up %s {$SHINYHUB_FORWARD_AUTH_SHARED_SECRET}</code></pre>
    <p class="note">Keep this credential between the reverse proxy and ShinyHub. Do not add it to browser or application code.</p>
  </section>
  <form class="actions" method="get">
    <button type="submit">Try again</button>
    <span class="http-status">HTTP 503 · Service unavailable</span>
  </form>
</main>
</body>
</html>`, favicon.Link(favicon.PlatformURL), forwardAuthProxyErrorCSS, html.EscapeString(lead), header, header)
}

func requestWantsHTML(r *http.Request) bool {
	return r.Header.Get("Sec-Fetch-Mode") == "navigate" || strings.Contains(r.Header.Get("Accept"), "text/html")
}
