import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import vm from 'node:vm';
import { JSDOM } from 'jsdom';
const source = readFileSync(new URL('../static/app.js', import.meta.url), 'utf8');
const html = readFileSync(new URL('../static/index.html', import.meta.url), 'utf8');
const start = source.indexOf('  function uploadDataFile(');
const productionHandler = source.slice(start, source.indexOf('  const accessLists =', start));
for (const kind of ['error', 'timeout', 'abort']) test(`data upload ${kind} retains the selected input and announces an outcome check`, t => {
  const dom = new JSDOM(html); t.after(() => dom.window.close()); const doc = dom.window.document;
  const file = { name: 'sample.csv' }; const input = doc.querySelector('#data-file-input');
  Object.defineProperty(input, 'files', { value: [file] }); let xhr, sends = 0;
  const context = vm.createContext({ document: doc, settingsSlug: 'example',
    accessLists: { capture: () => () => true }, encodeDataPath: value => value, readCookie: () => '',
    setError: (el, message) => { el.textContent = message; el.hidden = !message; },
    XMLHttpRequest: class { constructor() { xhr = this; this.upload = { addEventListener() {} }; } open() {} setRequestHeader() {} send() { sends++; } },
  });
  vm.runInContext(productionHandler, context); vm.runInContext('uploadDataFile({preventDefault(){}}); uploadDataFile({preventDefault(){}})', context);
  assert.equal(sends, 1); xhr['on' + kind](); xhr.onloadend();
  assert.equal(input.files[0], file); assert.equal(doc.querySelector('#data-upload-btn').disabled, false);
  assert.equal(doc.querySelector('#data-error').getAttribute('role'), 'alert');
  assert.match(doc.querySelector('#data-error').textContent, /Refresh the data list/);
});
