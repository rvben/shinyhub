// connectivityBanner builds the amber "realtime connection not established"
// warning shown at the top of the app detail Overview when an app is serving
// pages but no WebSocket has connected. That is the signature of a reverse proxy
// blocking the WebSocket upgrade: the app renders, but every Shiny interaction
// fails with "Shiny disconnected".
//
// It returns null when connectivity looks healthy (or the field is absent), so
// the caller can call it unconditionally and append the result. DOM helper: an
// explicit document is injected so it stays jsdom-testable, matching
// crash-banner.js and friends.
const DOCS_URL =
  'https://github.com/rvben/shinyhub/blob/main/docs/reverse-proxy/caddy.md#websockets-shiny-reactivity';

export function connectivityBanner(doc, envelope) {
  const conn = envelope && envelope.connectivity;
  if (!conn || !conn.serving_without_ws) return null;

  const banner = doc.createElement('div');
  banner.className = 'conn-banner';
  banner.setAttribute('role', 'alert');

  const title = doc.createElement('div');
  title.className = 'conn-banner-title';
  title.textContent = 'Realtime connection not established';
  banner.appendChild(title);

  const body = doc.createElement('p');
  body.className = 'conn-banner-body';
  body.textContent =
    'This app is serving pages, but no WebSocket has connected. Interactions ' +
    'will fail until it does - your reverse proxy may be blocking WebSocket ' +
    'upgrades.';
  banner.appendChild(body);

  const link = doc.createElement('a');
  link.className = 'conn-banner-link';
  link.href = DOCS_URL;
  link.target = '_blank';
  link.rel = 'noopener noreferrer';
  link.textContent = 'Check your reverse proxy (WebSockets) →';
  banner.appendChild(link);

  return banner;
}
