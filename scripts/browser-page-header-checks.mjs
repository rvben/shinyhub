import assert from 'node:assert/strict';

// Read-only DOM observation, usable by the browser suite and an interactive
// review. Check relationships rather than snapshots of individual pages.
export function readPageHeader() {
  const visible = el => el.getClientRects().length && getComputedStyle(el).visibility !== 'hidden';
  const header = [...document.querySelectorAll('main .page-header')].find(visible);
  if (!header) return null;
  const rect = el => {
    const r = el.getBoundingClientRect();
    return Object.fromEntries(['left', 'top', 'right', 'bottom', 'width', 'height'].map(k => [k, r[k]]));
  };
  const actions = header.querySelector('.page-actions');
  const controls = actions ? [...actions.querySelectorAll(':scope > button, :scope > a, :scope > .kebab-menu > button, :scope > .breadcrumb > a, :scope > .page-secondary > summary, :scope > .page-secondary > .page-secondary-panel > button')].filter(visible) : [];
  const page = header.closest('main > section');
  return {
    path: location.pathname,
    width: innerWidth,
    stacked: getComputedStyle(header).gridTemplateColumns.trim().split(/\s+/).length === 1,
    overflow: document.documentElement.scrollWidth > innerWidth,
    header: rect(header),
    page: rect(page),
    anchoredTitle: ['overview-heading', 'app-detail-heading'].includes(header.querySelector('h1').id),
    identityIcon: header.querySelector('.app-detail-icon-control') ? rect(header.querySelector('.app-detail-icon-control')) : null,
    heading: rect(header.querySelector('h1')),
    intro: rect(header.firstElementChild),
    controlHeight: +getComputedStyle(header).getPropertyValue('--page-control-h').trim().replace('px', ''),
    controls: controls.map(el => ({
      label: el.getAttribute('aria-label') || el.textContent.trim(),
      primary: el.classList.contains('btn-primary') || (innerWidth <= 640 && el.id === 'app-detail-open'),
      overflow: !!el.closest('.kebab-menu') || el.tagName === 'SUMMARY',
      ...rect(el),
    })),
    filtersInHeader: header.querySelectorAll('input, select').length,
    overflowingText: [...header.querySelectorAll('h1, .project-detail-description, .app-detail-description')]
      .filter(visible).filter(el => el.scrollWidth > el.clientWidth + 1).map(el => el.id || el.className),
    filters: [...page.querySelectorAll('.page-filters, .ov-scope')].filter(visible).map(rect),
  };
}

export function assertPageHeader(snapshot, label = '') {
  if (snapshot === null) return;
  assert.ok(snapshot, `${label}: page header observation must return a result`);
  const s = snapshot, context = `${label} ${s.path}`;
  const near = (a, b) => Math.abs(a - b) <= 1;
  assert.equal(s.overflow, false, `${context}: page must not overflow horizontally`);
  assert.equal(s.filtersInHeader, 0, `${context}: filters must be separate from page actions`);
  assert.equal(s.overflowingText.length, 0, `${context}: title and description text must fit their containers`);
  if (s.anchoredTitle) {
    if (s.identityIcon) {
      assert.ok(near(s.identityIcon.left, s.page.left), `${context}: app identity must start at the shared page left edge`);
      assert.ok(near(s.identityIcon.top, s.page.top), `${context}: avatar must align to the header top`);
      assert.ok(near(s.heading.left, s.identityIcon.right + 12), `${context}: app name must follow its avatar with a consistent gap`);
    } else {
      assert.ok(near(s.heading.left, s.page.left), `${context}: title must start at the shared page left edge`);
    }
    assert.ok(near(s.heading.top, s.page.top), `${context}: title must start at the shared page top edge`);
  }
  const ordinary = s.controls.filter(c => !c.overflow);
  const primary = ordinary.filter(c => c.primary);
  assert.ok(primary.length <= 1, `${context}: at most one primary page action`);
  if (primary.length) assert.equal(ordinary.at(-1), primary[0], `${context}: primary action follows secondary actions`);
  for (const c of s.controls) {
    assert.ok(near(c.height, s.controlHeight), `${context}: ${c.label} must share the page control height (${c.height})`);
    assert.ok(c.left >= s.header.left - 1 && c.right <= s.header.right + 1, `${context}: ${c.label} must fit the header`);
    if (!s.stacked) assert.ok(near(c.top, s.header.top), `${context}: ${c.label} must align to the header top`);
    else assert.ok(c.top >= s.intro.bottom + 11, `${context}: stacked actions need at least 12px below the title`);
  }
  if (s.controls.length) assert.ok(near(s.controls.at(-1).right, s.header.right), `${context}: actions must align to the page's right edge`);
  for (const f of s.filters) assert.ok(f.top >= s.header.bottom, `${context}: filters must follow the header`);
}
