import assert from 'node:assert/strict';

// Exercise the real controls, not selectOption(), which bypasses the popup.
// Geometry guards catch an OS picker, an overlapping popup, or a lost anchor.
export async function checkSelectPickers(page, origin, { width, theme }) {
  await page.goto(origin + '/apps/forecast/usage');
  const control = page.getByRole('combobox', { name: 'Usage reporting window', exact: true });
  await control.waitFor();
  const supported = await control.evaluate(el => getComputedStyle(el).appearance === 'base-select');
  if (!supported) {
    console.log('PICKER FALLBACK', theme, width, 'standard select retained');
    return;
  }
  const label = `${theme}/${width}`;
  await control.click();
  let geometry = await control.evaluate(readPicker);
  assert.ok(geometry.open, `${label}: picker must open`);
  assert.ok(geometry.first.top >= geometry.field.bottom + 4, `${label}: roomy picker must open below, separated from its field`);
  assert.ok(geometry.first.left >= 0 && geometry.first.right <= width, `${label}: picker must fit the viewport`);
  await page.getByRole('option', { name: '30 days', exact: true }).press('ArrowDown');
  await page.getByRole('option', { name: '90 days', exact: true }).press('Enter');
  assert.equal(await control.inputValue(), '90', `${label}: keyboard choice must reach the real select`);
  assert.equal(await control.evaluate(el => el.matches(':open')), false);

  // Resize while open to exercise the actual collision fallback without
  // injecting CSS or calling a separate test implementation of positioning.
  const viewport = page.viewportSize();
  await control.click();
  const field = await control.boundingBox();
  const shortHeight = Math.ceil(field.y + field.height + 60);
  await page.setViewportSize({ width, height: shortHeight });
  geometry = await control.evaluate(readPicker);
  assert.ok(geometry.last.bottom <= geometry.field.top - 4, `${label}: picker must flip above when it cannot fit below`);
  await page.getByRole('option', { name: '90 days', exact: true }).press('Escape');
  await page.setViewportSize(viewport);

  await page.goto(origin + '/tokens');
  await page.locator('#new-token-button').click();
  const expiry = page.getByRole('combobox', { name: 'Expires', exact: true });
  await expiry.click();
  await page.getByRole('option', { name: '90 days', exact: true }).press('Escape');
  assert.equal(await expiry.evaluate(el => el.matches(':open')), false);
  assert.equal(await page.locator('#new-token-modal').isVisible(), true,
    `${label}: first Escape must not dismiss the parent dialog`);
  assert.equal(await expiry.evaluate(el => document.activeElement === el), true,
    `${label}: focus must return to the field`);
  await expiry.press('Escape');
  assert.equal(await page.locator('#new-token-modal').isVisible(), false,
    `${label}: a second Escape must still dismiss the dialog`);
  console.log('PICKER PASS', label);
}

function readPicker(el) {
  return {
    open: el.matches(':open'),
    field: el.getBoundingClientRect().toJSON(),
    first: el.options[0].getBoundingClientRect().toJSON(),
    last: el.options[el.options.length - 1].getBoundingClientRect().toJSON(),
  };
}
