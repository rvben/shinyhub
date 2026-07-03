import { test } from 'node:test';
import assert from 'node:assert/strict';
import { JSDOM } from 'jsdom';
import { connectivityBanner } from '../static/views/connectivity-banner.js';

function doc() {
  return new JSDOM('<!DOCTYPE html><body></body>').window.document;
}

test('no banner when connectivity is absent or healthy', () => {
  // Missing envelope / missing field.
  assert.equal(connectivityBanner(doc(), {}), null);
  assert.equal(connectivityBanner(doc(), null), null);
  assert.equal(connectivityBanner(doc(), { connectivity: {} }), null);
  // WebSocket connected, or still within the grace window.
  assert.equal(
    connectivityBanner(doc(), { connectivity: { websocket_ok: true, serving_without_ws: false } }),
    null,
  );
  assert.equal(
    connectivityBanner(doc(), { connectivity: { websocket_ok: false, serving_without_ws: false } }),
    null,
  );
});

test('serving-without-ws renders the amber warning with a docs link', () => {
  const el = connectivityBanner(doc(), {
    connectivity: { websocket_ok: false, serving_without_ws: true },
  });

  assert.ok(el, 'a banner is returned');
  assert.equal(el.className, 'conn-banner');
  assert.equal(el.getAttribute('role'), 'alert', 'announced to assistive tech');
  assert.match(el.textContent, /Realtime connection not established/);
  assert.match(el.textContent, /reverse proxy may be blocking WebSocket/);

  const link = el.querySelector('.conn-banner-link');
  assert.ok(link, 'has a reverse-proxy docs link');
  assert.match(link.getAttribute('href'), /reverse-proxy\/caddy\.md#websockets/);
  assert.equal(link.getAttribute('rel'), 'noopener noreferrer', 'external link is safe');
});
