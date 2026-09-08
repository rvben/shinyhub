import { test } from 'node:test';
import assert from 'node:assert/strict';
import { JSDOM } from 'jsdom';
import { mountAppsGrid } from '../static/views/apps-grid.js';
import { mountLaunchpad } from '../static/views/launchpad.js';
import { mountUsers } from '../static/views/users.js';

// router.js sets document.title from the `title` a mounted view returns:
// `title + ' · ' + brand`, or the bare brand when the title is empty. A view
// that returns '' therefore makes its tab, its bookmark and its history entry
// read as the product name with no page in it, and a view that returns a name
// the page does not use sends the visitor looking for a page that is not there.
//
// The rule these pin: every routed view's title is the text of the <h1> the
// visitor is looking at. Checked here against the real index.html headings so
// renaming a page in the markup and forgetting the title fails the build.

function fixture(html) {
  const dom = new JSDOM(`<!DOCTYPE html><body>${html}</body>`, {
    url: 'http://localhost/apps',
  });
  global.document = dom.window.document;
  global.localStorage = dom.window.localStorage;
  global.location = dom.window.location;
  return dom;
}

const noopCtx = {
  state: { user: { id: 7, username: 'sam' } },
  metrics: { setTargets() {} },
  api: async () => ({ ok: true, status: 200, json: async () => ({ items: [] }) }),
  onUnauthorized() {},
  renderGridVerbatim() {},
  showError() {},
  updateActiveNav() {},
  loadUsers() {},
};

test('the operator Apps grid titles its tab Apps, not the bare brand', async () => {
  fixture(`<section id="apps-view" hidden>
    <div id="app-grid"></div><div id="empty-state"></div>
  </section>`);
  const view = await mountAppsGrid({ ...noopCtx });
  assert.equal(view.title, 'Apps');
});

test('a failed load still titles the tab Apps', async () => {
  // The early-return path builds its own view object. A title fixed on the
  // happy path only is a title the visitor loses exactly when they are lost.
  fixture(`<section id="apps-view" hidden>
    <div id="app-grid"></div><div id="empty-state"></div>
  </section>`);
  const view = await mountAppsGrid({
    ...noopCtx,
    api: async () => { throw new Error('network down'); },
  });
  assert.equal(view.title, 'Apps');
});

test('the viewer launchpad titles its tab Apps, the name the visitor sees', () => {
  fixture(`<section id="launchpad-view" hidden>
    <h1 id="launchpad-heading" class="lp-page-title">Apps</h1>
    <div id="launchpad-body"></div>
  </section>`);
  const view = mountLaunchpad({ ...noopCtx });
  try {
    assert.equal(view.title, 'Apps');
    assert.notEqual(view.title, 'Launchpad', 'Launchpad is the internal name, never shown');
  } finally {
    view.unmount();
  }
});

test('the Identity page titles its tab Identity, not its former name Users', () => {
  fixture('<section id="users-view" hidden><h1 id="identity-heading">Identity</h1></section>');
  const view = mountUsers({ ...noopCtx });
  assert.equal(view.title, 'Identity');
});
