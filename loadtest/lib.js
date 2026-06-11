/**
 * loadtest/lib.js - shared helpers for ShinyHub k6 scenarios.
 */

/**
 * LOADING_MARKER is the stable HTML fragment that identifies ShinyHub's
 * hibernation loading page (internal/proxy/proxy.go, loadingPage const).
 * A Go contract test in internal/proxy/loading_contract_test.go pins both
 * this literal and the Go source simultaneously, so a loading-page redesign
 * that removes the element fails the build rather than silently invalidating
 * load-test assertions.
 */
export const LOADING_MARKER = 'id="shinyhub-box"';

/**
 * appURL builds the full URL for an app path.
 *
 * @param {string} host   - base host, e.g. "http://127.0.0.1:8080"
 * @param {string} slug   - app slug, e.g. "myapp"
 * @param {string} path   - path beneath the app root, e.g. "/" or "/.shinyhub/ready"
 * @returns {string}
 */
export function appURL(host, slug, path) {
  const base = host.replace(/\/+$/, '');
  const p = path.startsWith('/') ? path : '/' + path;
  return `${base}/app/${slug}${p}`;
}

/**
 * wsURL builds a WebSocket URL for an app, swapping http(s) to ws(s).
 *
 * @param {string} host   - base host, e.g. "http://127.0.0.1:8080"
 * @param {string} slug   - app slug
 * @param {string} wsPath - WS endpoint beneath the app root, e.g. "/websocket/"
 * @returns {string}
 */
export function wsURL(host, slug, wsPath) {
  const url = appURL(host, slug, wsPath);
  return url.replace(/^http:/, 'ws:').replace(/^https:/, 'wss:');
}

/**
 * cookieHeaderFromJar merges cookies from a k6 http.cookieJar() for the
 * given URL with an optional extra Cookie header string (LT_AUTH_COOKIE).
 * Returns a single "Cookie" header value suitable for use in WS params.headers.
 *
 * @param {object} jar   - k6 http.cookieJar()
 * @param {string} url   - the app URL (used to scope jar lookup)
 * @param {string} extra - optional extra Cookie header value (may be empty/null)
 * @returns {string}
 */
export function cookieHeaderFromJar(jar, url, extra) {
  const parts = [];

  const jarCookies = jar.cookiesForURL(url);
  for (const [name, values] of Object.entries(jarCookies)) {
    for (const v of values) {
      parts.push(`${name}=${v}`);
    }
  }

  if (extra && extra.trim() !== '') {
    parts.push(extra.trim());
  }

  return parts.join('; ');
}
