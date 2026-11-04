import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { JSDOM } from 'jsdom';
import { deployCommands, cliSetupCommands, tokenUsageCommand } from '../static/views/deploy-commands.js';

test('deployment commands target the current hub and selected app', () => {
  assert.equal(deployCommands('http://127.0.0.1:8771', 'forecast'),
    'shinyhub deploy . --slug forecast --wait --host http://127.0.0.1:8771');
  assert.match(deployCommands('https://hub.example.com', 'revenue'), /--slug revenue --wait --host https:\/\/hub.example.com$/);
  assert.match(deployCommands('http://[::1]:8771', ''), /--host 'http:\/\/\[::1\]:8771'/);
  assert.match(deployCommands('https://hub.example.com', ''), /--slug '<slug>'/);
});

test('deployment opens with CLI guidance and keeps upload behind a closed fallback', () => {
  const html = readFileSync(new URL('../static/index.html', import.meta.url), 'utf8');
  const doc = new JSDOM(html).window.document;
  const modal = doc.getElementById('deploy-modal');
  const guide = modal.querySelector('.deploy-cli-guide');
  const fallback = modal.querySelector('#deploy-upload-fallback');
  assert.equal(fallback.open, false);
  assert.equal(modal.querySelector('#deploy-cli-setup').open, false);
  assert.ok(modal.querySelector('#deploy-cli-setup #deploy-cli-setup-copy'));
  assert.match(guide.textContent, /Recommended for production/);
  assert.ok(guide.compareDocumentPosition(fallback) & 4);
  assert.ok(guide.querySelector('#deploy-cli-snippet-copy'));
  for (const id of ['deploy-pick', 'deploy-file']) {
    assert.ok(fallback.querySelector(`#${id}`), `${id} stays within the explicit fallback`);
  }
  const feedback = modal.querySelector('#deploy-upload-state');
  assert.equal(feedback.hidden, true);
  for (const id of ['deploy-submit', 'deploy-progress-wrap', 'deploy-result', 'deploy-error']) {
    assert.ok(feedback.querySelector(`#${id}`), `${id} stays hidden until a browser bundle is selected`);
  }
  assert.match(fallback.textContent, /not recommended for production/);
});


test('installation and connection belong only to optional first-time setup', () => {
  const deploy = deployCommands('https://hub.example.com', 'forecast');
  assert.doesNotMatch(deploy, /uv tool|shinyhub connect/);
  assert.match(deploy, /--host https:\/\/hub.example.com/);
  assert.equal(cliSetupCommands('https://hub.example.com'), 'uv tool install shinyhub\nshinyhub connect https://hub.example.com');
});

test('manual token example quotes unusual hub addresses and keeps credentials as a placeholder', () => {
  assert.equal(tokenUsageCommand('http://[::1]:8771').replaceAll(/\\\n\s*/g,''), "SHINYHUB_TOKEN='<token>' shinyhub apps list --host 'http://[::1]:8771'");
});
