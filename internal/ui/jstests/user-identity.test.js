import { test } from 'node:test';
import assert from 'node:assert/strict';
import { initials, avatarHue, identityModel } from '../static/views/user-identity.js';

test('initials prefer first+last word of a display name', () => {
  assert.equal(initials('Ruben Jongejan', 'ruben'), 'RJ');
  assert.equal(initials('  ada   lovelace  ', 'ada'), 'AL');
  assert.equal(initials('Jean-Luc Picard', 'jlp'), 'JP'); // first alnum of each word
});

test('initials fall back through single token, username, then ?', () => {
  assert.equal(initials('Madonna', 'madonna'), 'MA'); // first two of single token
  assert.equal(initials('', 'alice'), 'AL'); // no display name -> username
  assert.equal(initials('', ''), '?'); // nothing at all
  assert.equal(initials('   ', '   '), '?'); // whitespace only
});

test('initials ignore leading punctuation/emoji and uppercase the result', () => {
  assert.equal(initials('', '_deploy'), 'DE');
  assert.equal(initials('小 明', 'xm'), 'XM'); // no latin alnum in name -> username
});

test('avatarHue is deterministic and bounded to [0,360)', () => {
  const a = avatarHue('ruben');
  assert.equal(a, avatarHue('ruben')); // stable across calls
  assert.ok(a >= 0 && a < 360);
  assert.notEqual(avatarHue('alice'), avatarHue('bob')); // different seeds differ
  assert.equal(avatarHue(''), avatarHue(null)); // null coerces to empty
});

test('identityModel falls back to username when no display name', () => {
  const m = identityModel({ username: 'alice', role: 'admin', display_name: '' });
  assert.equal(m.name, 'alice');
  assert.equal(m.secondary, ''); // primary line already is the username
  assert.equal(m.roleLabel, 'Admin');
  assert.equal(m.initials, 'AL');
});

test('identityModel shows username as secondary line when a display name exists', () => {
  const m = identityModel({ username: 'rjongejan', role: 'developer', display_name: 'Ruben Jongejan' });
  assert.equal(m.name, 'Ruben Jongejan');
  assert.equal(m.secondary, 'rjongejan');
  assert.equal(m.roleLabel, 'Developer');
  assert.equal(m.initials, 'RJ');
});

test('identityModel hue is seeded by username so it is stable across name edits', () => {
  const a = identityModel({ username: 'sam', role: 'viewer', display_name: 'Samuel Vimes' });
  const b = identityModel({ username: 'sam', role: 'viewer', display_name: 'Sam' });
  assert.equal(a.hue, b.hue); // same user, same color regardless of display name
});

test('identityModel tolerates a null/empty user', () => {
  const m = identityModel(null);
  assert.equal(m.name, 'Unknown');
  assert.equal(m.initials, '?');
  assert.equal(m.roleLabel, '');
});
