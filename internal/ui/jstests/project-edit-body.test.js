import { test } from 'node:test';
import assert from 'node:assert/strict';
import { buildProjectPatchBody } from '../static/views/project-edit-body.js';

// buildProjectPatchBody is the single source of truth for what the "Edit
// project" modal sends to PATCH /api/projects/{slug}. The server's PATCH
// contract is declared-only: an absent key means "leave alone", a present
// key - including "" - means "set it". These tests exist because a prior
// version of this logic sent description unconditionally, which silently
// wiped an existing description on a no-op edit (open, change nothing,
// save). Asserting `'description' in body` rather than
// `body.description === undefined` matters: an object literal can carry a
// key whose value is undefined, which still passes the latter check but
// still serializes away via JSON.stringify, reintroducing the exact bug
// through a different route.

test('descriptionKnown false omits the description key entirely', () => {
  const body = buildProjectPatchBody({
    name: 'Analytics',
    iconEmoji: '',
    description: '',
    descriptionKnown: false,
  });
  assert.equal('description' in body, false);
});

test('descriptionKnown true with a non-empty value includes it', () => {
  const body = buildProjectPatchBody({
    name: 'Analytics',
    iconEmoji: '',
    description: 'Quarterly reporting apps',
    descriptionKnown: true,
  });
  assert.equal('description' in body, true);
  assert.equal(body.description, 'Quarterly reporting apps');
});

test('descriptionKnown true with an empty string still includes it (explicit clear)', () => {
  const body = buildProjectPatchBody({
    name: 'Analytics',
    iconEmoji: '',
    description: '',
    descriptionKnown: true,
  });
  assert.equal('description' in body, true);
  assert.equal(body.description, '');
});

test('name and icon_emoji are always present', () => {
  const withoutDescription = buildProjectPatchBody({
    name: 'Analytics',
    iconEmoji: '📊',
    description: '',
    descriptionKnown: false,
  });
  assert.equal(withoutDescription.name, 'Analytics');
  assert.equal(withoutDescription.icon_emoji, '📊');

  const withDescription = buildProjectPatchBody({
    name: 'Finance',
    iconEmoji: '💰',
    description: 'Budget apps',
    descriptionKnown: true,
  });
  assert.equal(withDescription.name, 'Finance');
  assert.equal(withDescription.icon_emoji, '💰');
});
