import { test } from 'node:test';
import assert from 'node:assert/strict';
import { RENDER_SECONDS_MAX, parseRenderSeconds, renderPacingAdvice } from '../static/views/render-pacing.js';

// parseRenderSeconds + renderPacingAdvice back the Configuration -> Render
// pacing control. Both are DOM-free, so these run without jsdom.
//
// The server-side block they consume (render_pacing) is built by
// internal/api/render_pacing.go and is ABSENT when the saved render_seconds is
// 0, which is why "no block" is a meaningful input here rather than an error.

const BLOCK = {
  render_seconds: 1.5,
  effective_cores: 4,
  cores_source: 'cgroup-quota',
  suggested_max_sessions_per_replica: 4,
  current_effective_max_sessions_per_replica: 20,
  cadence_assumption_seconds: 2,
};

// --- parseRenderSeconds ---

test('parses a decimal value', () => {
  assert.deepEqual(parseRenderSeconds('1.5'), { ok: true, value: 1.5 });
});

test('parses 0 as a valid value (pacing off)', () => {
  assert.deepEqual(parseRenderSeconds('0'), { ok: true, value: 0 });
});

test('trims surrounding whitespace', () => {
  assert.deepEqual(parseRenderSeconds('  2 '), { ok: true, value: 2 });
});

test('accepts the documented maximum', () => {
  assert.deepEqual(parseRenderSeconds(String(RENDER_SECONDS_MAX)), { ok: true, value: 600 });
});

// A blank field must NOT be read as 0. Blank and zero look identical in a number
// input but mean different things, and coercing blank to 0 would turn an
// accidental clear into a silent "turn pacing off" write.
test('blank is rejected, not coerced to 0', () => {
  const got = parseRenderSeconds('');
  assert.equal(got.ok, false);
  assert.match(got.error, /Use 0 to turn pacing off/);
});

test('whitespace-only is rejected like blank', () => {
  assert.equal(parseRenderSeconds('   ').ok, false);
});

test('null and undefined are rejected like blank', () => {
  assert.equal(parseRenderSeconds(null).ok, false);
  assert.equal(parseRenderSeconds(undefined).ok, false);
});

test('non-numeric text is rejected', () => {
  const got = parseRenderSeconds('fast');
  assert.equal(got.ok, false);
  assert.match(got.error, /must be a number/);
});

test('Infinity is rejected as non-finite', () => {
  assert.equal(parseRenderSeconds('Infinity').ok, false);
  assert.equal(parseRenderSeconds('1e999').ok, false);
});

test('a negative value is rejected', () => {
  const got = parseRenderSeconds('-1');
  assert.equal(got.ok, false);
  assert.match(got.error, /between 0 and 600/);
});

test('a value above the maximum is rejected', () => {
  assert.equal(parseRenderSeconds('600.1').ok, false);
});

// --- renderPacingAdvice ---

test('no block and 0 typed reports pacing is off, in the present tense', () => {
  assert.equal(
    renderPacingAdvice(0, null),
    'Pacing is off. Page loads are admitted as fast as they arrive.',
  );
});

test('a block plus 0 typed reports what SAVING would do, not the current state', () => {
  assert.equal(
    renderPacingAdvice(0, BLOCK),
    'Saving 0 turns pacing off. Page loads will be admitted as fast as they arrive.',
  );
});

test('no block and a positive value typed reports turning pacing on', () => {
  assert.equal(
    renderPacingAdvice(1.5, null),
    'Saving turns pacing on at 1.5s per render.',
  );
});

// While an operator is mid-edit, the block (which describes the SAVED value) and
// the typed value disagree. Quoting a cap suggestion derived from the old number
// against the new one would read as a prediction about the new one, so the
// advice says only what saving will change.
test('a typed value that differs from the saved one quotes neither cores nor a cap', () => {
  const line = renderPacingAdvice(3, BLOCK);
  assert.equal(line, 'Saving changes pacing from 1.5s to 3s per render.');
  assert.doesNotMatch(line, /cores/);
  assert.doesNotMatch(line, /sessions per replica/);
});

test('the typed value matching the saved one shows the full advisory', () => {
  assert.equal(
    renderPacingAdvice(1.5, BLOCK),
    'Paced for ~4 cores (cgroup-quota). For sustained load, consider lowering max sessions per replica to 4 (currently 20). Assumes one render per session every 2s.',
  );
});

test('float noise between typed and saved still counts as unchanged', () => {
  const line = renderPacingAdvice(0.1 + 0.2, { ...BLOCK, render_seconds: 0.3 });
  assert.match(line, /^Paced for /);
});

test('a suggested cap at or above the current one is reported as within budget', () => {
  const line = renderPacingAdvice(1.5, {
    ...BLOCK,
    suggested_max_sessions_per_replica: 40,
    current_effective_max_sessions_per_replica: 20,
  });
  assert.match(line, /The current max of 20 sessions per replica is within what this host sustains\./);
  assert.doesNotMatch(line, /consider lowering/);
});

// A cap of 0 is unlimited, not "zero sessions": it is the state the suggestion
// exists to warn about, and reading it as a number smaller than the suggestion
// would tell the least protected app it is within budget.
test('an unlimited current cap takes the suggestion branch and is named, not printed as 0', () => {
  const line = renderPacingAdvice(1.5, {
    ...BLOCK,
    suggested_max_sessions_per_replica: 4,
    current_effective_max_sessions_per_replica: 0,
  });
  assert.match(
    line,
    /For sustained load, consider setting max sessions per replica to 4 \(currently unlimited\)\./,
  );
  assert.doesNotMatch(line, /within what this host sustains/);
  assert.doesNotMatch(line, /currently 0/);
  // The cadence caveat still applies: a suggestion without its assumption is a
  // number the operator cannot judge.
  assert.match(line, /Assumes one render per session every 2s\./);
});

test('a whole-number render cost renders without a trailing zero', () => {
  const line = renderPacingAdvice(2, { ...BLOCK, render_seconds: 1 });
  assert.equal(line, 'Saving changes pacing from 1s to 2s per render.');
});

test('fractional cores render as typed, not rounded away', () => {
  const line = renderPacingAdvice(1.5, { ...BLOCK, effective_cores: 2.5 });
  assert.match(line, /^Paced for ~2\.5 cores /);
});

test('a missing cores_source omits the parenthetical rather than printing an empty one', () => {
  const line = renderPacingAdvice(1.5, { ...BLOCK, cores_source: '' });
  assert.match(line, /^Paced for ~4 cores\. /);
  assert.doesNotMatch(line, /\(\)/);
});

// A block with no usable core count still means pacing is on; the advice degrades
// to that fact rather than claiming "~0 cores" or going blank.
test('a zero core count degrades to a bare pacing-on statement', () => {
  const line = renderPacingAdvice(1.5, { ...BLOCK, effective_cores: 0 });
  assert.match(line, /^Pacing is on\./);
  assert.doesNotMatch(line, /cores/);
});

test('a missing cap suggestion omits the cap sentence and its cadence caveat', () => {
  const line = renderPacingAdvice(1.5, { ...BLOCK, suggested_max_sessions_per_replica: 0 });
  assert.equal(line, 'Paced for ~4 cores (cgroup-quota).');
});

test('a non-finite typed value yields no advice at all', () => {
  assert.equal(renderPacingAdvice(NaN, BLOCK), '');
  assert.equal(renderPacingAdvice(Infinity, BLOCK), '');
});

test('a non-object block is treated as absent', () => {
  assert.equal(
    renderPacingAdvice(0, 'render_pacing'),
    'Pacing is off. Page loads are admitted as fast as they arrive.',
  );
  assert.equal(renderPacingAdvice(1.5, undefined), 'Saving turns pacing on at 1.5s per render.');
});
