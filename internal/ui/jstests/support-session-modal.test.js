import { test } from 'node:test';
import assert from 'node:assert/strict';
import { JSDOM } from 'jsdom';
import {
  clearSupportAppOwnedError,
  consumeSupportDraft,
  createSupportAppRequestGate,
  createSupportSessionModalLock,
  isFreshSupportDraft,
  restoreSupportAppSelection,
  restoreFailedActionFocus,
  saveSupportDraft,
} from '../static/views/support-session-modal.js';

function fixture() {
  const dom = new JSDOM('<div id="modal"><button id="close">Close</button><button id="cancel">Cancel</button></div>');
  const document = dom.window.document;
  const modal = document.getElementById('modal');
  const closeButton = document.getElementById('close');
  const cancelButton = document.getElementById('cancel');
  return { dom, modal, closeButton, cancelButton,
    lock: createSupportSessionModalLock({ modal, closeButton, cancelButton }) };
}

test('pending support-session creation locks every dismissal control', () => {
  const view = fixture();
  let dismissals = 0;
  view.lock.setPending(true);
  assert.equal(view.modal.getAttribute('aria-busy'), 'true');
  assert.equal(view.closeButton.disabled, true);
  assert.equal(view.cancelButton.disabled, true);
  assert.equal(view.lock.requestDismiss(() => { dismissals += 1; }), false);
  assert.equal(dismissals, 0);
  view.dom.window.close();
});

test('failed creation restores dismissal and removes busy state', () => {
  const view = fixture();
  let dismissals = 0;
  view.lock.setPending(true);
  view.lock.setPending(false);
  assert.equal(view.modal.hasAttribute('aria-busy'), false);
  assert.equal(view.closeButton.disabled, false);
  assert.equal(view.cancelButton.disabled, false);
  assert.equal(view.lock.requestDismiss(() => { dismissals += 1; }), true);
  assert.equal(dismissals, 1);
  view.dom.window.close();
});

test('eligible-app request gate aborts and rejects stale same-user responses', () => {
  const gate = createSupportAppRequestGate();
  const first = gate.begin();
  const second = gate.begin();
  assert.equal(first.signal.aborted, true);
  assert.equal(gate.isCurrent(first.generation), false);
  assert.equal(gate.isCurrent(second.generation), true);
  gate.invalidate();
  assert.equal(second.signal.aborted, true);
  assert.equal(gate.isCurrent(second.generation), false);
});

test('fresh-auth recovery round-trips and consumes the pending draft', () => {
  const dom = new JSDOM('<!doctype html>', { url: 'https://hub.example.com/users' });
  const draft = { user_id: 42, username: 'alice', app_slug: 'sales', reason: 'Investigating SUP-42', saved_at: Date.now() };
  assert.equal(saveSupportDraft(dom.window.sessionStorage, draft), true);
  assert.deepEqual(consumeSupportDraft(dom.window.sessionStorage), draft);
  assert.equal(consumeSupportDraft(dom.window.sessionStorage), null, 'draft must not reopen twice');
  dom.window.close();
});

test('draft persistence failure is explicit and stale drafts expire', () => {
  const blocked = { setItem() { throw new Error('quota'); } };
  assert.equal(saveSupportDraft(blocked, { saved_at: 100 }), false);
  assert.equal(isFreshSupportDraft({ saved_at: 100 }, 200), true);
  assert.equal(isFreshSupportDraft({ saved_at: 100 }, 10 * 60 * 1000 + 101), false);
  assert.equal(isFreshSupportDraft({ saved_at: 300 }, 200), false);
});

test('draft recovery restores its app even after another control was edited', () => {
  const dom = new JSDOM('<select><option value="other">Other</option><option value="sales">Sales</option></select><button>Start</button>');
  const select = dom.window.document.querySelector('select');
  const submit = dom.window.document.querySelector('button');
  select.value = 'other';
  assert.equal(restoreSupportAppSelection(select, submit, 'sales'), true);
  assert.equal(select.value, 'sales');
  dom.window.close();
});

test('draft recovery requires an explicit choice when its app is no longer eligible', () => {
  const dom = new JSDOM('<button id="close">Close</button><select aria-describedby="status"><option value="other">Other</option></select><p id="error" hidden></p><button id="submit">Start</button>');
  const select = dom.window.document.querySelector('select');
  const submit = dom.window.document.getElementById('submit');
  dom.window.document.getElementById('close').focus();
  assert.equal(restoreSupportAppSelection(select, submit, 'missing', { focusOnMissing: true }), false);
  assert.equal(select.value, '');
  assert.equal(submit.disabled, true);
  assert.equal(select.getAttribute('aria-invalid'), 'true');
  assert.equal(select.getAttribute('aria-describedby'), 'status');
  assert.equal(select.getAttribute('aria-errormessage'), 'support-session-error');
  assert.equal(dom.window.document.activeElement, select);
  assert.match(select.selectedOptions[0].textContent, /previous selection unavailable/i);
  dom.window.close();
});

test('App change clears only an App-owned shared error', () => {
  const dom = new JSDOM('<select aria-invalid="true" aria-errormessage="support-session-error"></select><p id="error">Choose another app.</p>');
  const select = dom.window.document.querySelector('select');
  const error = dom.window.document.getElementById('error');
  assert.equal(clearSupportAppOwnedError(select, error), true);
  assert.equal(error.hidden, true);
  assert.equal(select.hasAttribute('aria-invalid'), false);
  error.hidden = false;
  error.textContent = 'Reason is too short.';
  assert.equal(clearSupportAppOwnedError(select, error), false);
  assert.equal(error.hidden, false, 'an unrelated shared error must remain visible');
  dom.window.close();
});

test('failed async action restores focus only when that action owned it', () => {
  const dom = new JSDOM('<body tabindex="-1"><button id="retry">Retry</button><button id="other">Other</button></body>');
  const retry = dom.window.document.getElementById('retry');
  const other = dom.window.document.getElementById('other');
  retry.focus();
  const ownedBeforeDisable = dom.window.document.activeElement === retry;
  retry.disabled = true;
  dom.window.document.body.focus(); // model browsers that blur disabled controls
  retry.disabled = false;
  assert.equal(restoreFailedActionFocus(retry, ownedBeforeDisable), true);
  assert.equal(dom.window.document.activeElement, retry);
  other.focus();
  assert.equal(restoreFailedActionFocus(retry, true), false);
  assert.equal(dom.window.document.activeElement, other, 'must not steal focus after user movement');
  dom.window.close();
});
