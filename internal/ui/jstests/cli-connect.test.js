import test from 'node:test';
import assert from 'node:assert/strict';

import {
  cliConnectDeviceLabel,
  legacyCLIConnectLinkDetected,
  normalizeCLIUserCode,
  validCLIUserCode,
} from '../static/views/cli-connect.js';

test('typed codes are normalized regardless of case, spacing, or dash placement', () => {
  assert.equal(normalizeCLIUserCode('abcd1234'), 'ABCD-1234');
  assert.equal(normalizeCLIUserCode('AbCd-1234'), 'ABCD-1234');
  assert.equal(normalizeCLIUserCode(' abcd 1234 '), 'ABCD-1234');
  assert.equal(normalizeCLIUserCode('abcd-12-34'), 'ABCD-1234');
});

test('a partial code is left unpadded so the input keeps accepting keystrokes', () => {
  assert.equal(normalizeCLIUserCode('abc'), 'ABC');
  assert.equal(normalizeCLIUserCode(''), '');
  assert.equal(normalizeCLIUserCode(undefined), '');
});

test('only the exact XXXX-XXXX shape validates', () => {
  assert.equal(validCLIUserCode('ABCD-1234'), true);
  assert.equal(validCLIUserCode('ABCD1234'), false);
  assert.equal(validCLIUserCode('ABC-1234'), false);
  assert.equal(validCLIUserCode('ABCD-123'), false);
  assert.equal(validCLIUserCode(''), false);
  assert.equal(validCLIUserCode(null), false);
});

test('the generated token suffix is removed from the device label', () => {
  assert.equal(cliConnectDeviceLabel('cli-rubens-macbook-a1b2c3'), 'rubens-macbook');
  assert.equal(cliConnectDeviceLabel(''), 'terminal');
});

test('a legacy CLI authorization link is detected by its old query parameters', () => {
  assert.equal(legacyCLIConnectLinkDetected('?connect_hash=deadbeef'), true);
  assert.equal(legacyCLIConnectLinkDetected('?connect_name=laptop'), true);
  assert.equal(legacyCLIConnectLinkDetected('?connect_code=ABCD1234'), true);
  assert.equal(legacyCLIConnectLinkDetected('?connect_hash=x&connect_name=y'), true);
});

test('an ordinary /tokens visit is not flagged as a legacy link', () => {
  assert.equal(legacyCLIConnectLinkDetected(''), false);
  assert.equal(legacyCLIConnectLinkDetected('?'), false);
  assert.equal(legacyCLIConnectLinkDetected('?tab=tokens'), false);
  assert.equal(legacyCLIConnectLinkDetected(undefined), false);
});
