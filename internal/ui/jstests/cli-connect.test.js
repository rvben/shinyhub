import test from 'node:test';
import assert from 'node:assert/strict';

import {
  cliConnectDeviceLabel,
  cliConnectRequestFromSearch,
  validCLIConnectRequest,
} from '../static/views/cli-connect.js';

const hash = 'a'.repeat(64);

test('a complete terminal pairing request is parsed', () => {
  assert.deepEqual(
    cliConnectRequestFromSearch(`?connect_hash=${hash}&connect_name=cli-workstation-a1b2c3&connect_code=7AF3-20BD`),
    { tokenHash: hash, name: 'cli-workstation-a1b2c3', code: '7AF3-20BD' },
  );
});

test('partial, malformed, and oversized pairing requests are ignored', () => {
  for (const search of [
    '',
    `?connect_hash=${hash}`,
    `?connect_hash=raw-secret&connect_name=cli-x-a1b2c3&connect_code=7AF3-20BD`,
    `?connect_hash=${hash}&connect_name=cli-x-a1b2c3&connect_code=not-a-code`,
    `?connect_hash=${hash}&connect_name=${'x'.repeat(65)}&connect_code=7AF3-20BD`,
  ]) {
    assert.equal(cliConnectRequestFromSearch(search), null, search);
  }
});

test('stored requests are validated before restoration', () => {
  assert.equal(validCLIConnectRequest({ tokenHash: hash, name: 'cli-laptop-a1b2c3', code: '7AF3-20BD' }), true);
  assert.equal(validCLIConnectRequest({ tokenHash: hash, name: '', code: '7AF3-20BD' }), false);
  assert.equal(validCLIConnectRequest(null), false);
});

test('the generated token suffix is removed from the device label', () => {
  assert.equal(cliConnectDeviceLabel('cli-rubens-macbook-a1b2c3'), 'rubens-macbook');
  assert.equal(cliConnectDeviceLabel(''), 'terminal');
});
