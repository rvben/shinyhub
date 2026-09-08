import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { JSDOM } from 'jsdom';

// Opening a modal marks the background inert. `inert` is inherited by every
// descendant, so a modal that lives *inside* the region being inerted disables
// itself the moment it opens: its inputs cannot be focused or filled, its
// buttons cannot be clicked, and the whole subtree drops out of the
// accessibility tree. Only Escape still works, because that listener sits
// outside the subtree.
//
// The invariant that keeps every modal usable is purely structural - no modal
// may be a descendant of a region that setModalBackgroundInert() marks - so it
// is pinned here against the real markup rather than left to authoring care.
const indexHTML = readFileSync(new URL('../static/index.html', import.meta.url), 'utf8');
const appJS = readFileSync(new URL('../static/app.js', import.meta.url), 'utf8');

// The regions are read out of app.js instead of being restated, so adding a
// third inerted region without moving the modals out of it fails here too.
function inertedRegionIds(source) {
  const fn = source.slice(source.indexOf('function setModalBackgroundInert'));
  const body = fn.slice(0, fn.indexOf('\n}'));
  return [...body.matchAll(/getElementById\('([^']+)'\)/g)].map((m) => m[1]);
}

test('setModalBackgroundInert targets the regions this test guards', () => {
  const ids = inertedRegionIds(appJS);
  assert.ok(ids.length > 0, 'no inerted regions found; the guard below would be vacuous');
  assert.deepEqual(ids, ['app-shell', 'mobile-topbar']);
});

test('no modal is a descendant of a region that gets marked inert', () => {
  const { window } = new JSDOM(indexHTML);
  const doc = window.document;
  const modals = [...doc.querySelectorAll('.modal-overlay')];
  assert.ok(modals.length >= 11, `expected the full modal set, found ${modals.length}`);

  const offenders = [];
  for (const id of inertedRegionIds(appJS)) {
    const region = doc.getElementById(id);
    if (!region) continue;
    for (const modal of modals) {
      if (region.contains(modal)) offenders.push(`#${modal.id || '(no id)'} inside #${id}`);
    }
  }
  assert.deepEqual(
    offenders,
    [],
    `these modals inert themselves when opened, leaving Escape as the only working control:\n  ${offenders.join('\n  ')}`,
  );
});

// A modal must also stay reachable as a body-level element, so the overlay's
// fixed positioning is not trapped in an ancestor's containing block.
test('every modal is a direct child of body', () => {
  const { window } = new JSDOM(indexHTML);
  const doc = window.document;
  const stray = [...doc.querySelectorAll('.modal-overlay')]
    .filter((m) => m.parentElement !== doc.body)
    .map((m) => `#${m.id || '(no id)'} under <${m.parentElement.tagName.toLowerCase()}>`);
  assert.deepEqual(stray, [], `modals must be body-level:\n  ${stray.join('\n  ')}`);
});
