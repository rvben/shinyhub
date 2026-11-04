// Inherited values stay visible and read-only until a custom value is enabled.
// Explicit no-limit values remain distinct from following a server default.
export const directSettings = {
  'resources-memory': { inherit: null, off: 0, offLabel: 'No limit', unit: 'MB', min: 16, max: 1048576, label: 'Memory limit' },
  'resources-cpu': { inherit: null, off: 0, offLabel: 'No limit', unit: 'cores', factor: 0.01, min: 0.01, max: 64, label: 'CPU limit', decimal: true, precision: 2 },
  'scaling-cap': { inherit: 0, unit: 'sessions', min: 1, max: 1000, label: 'Session limit' },
  'hibernate-custom-minutes': { inherit: null, off: 0, offLabel: 'Never hibernate', unit: 'minutes', min: 1, max: Number.MAX_SAFE_INTEGER, label: 'Idle timeout' },
  'autoscale-target': { inherit: 0, unit: '%', factor: 100, min: 1, max: 100, label: 'Target session load', decimal: true },
  'render-seconds': { off: 0, offLabel: 'Off', unit: 'seconds', min: Number.MIN_VALUE, max: 600, label: 'Render cost', decimal: true },
};
const has = (object, key) => Object.hasOwn(object, key);
const elements = (doc, id) => ({ input: doc.getElementById(id), mode: doc.getElementById(`${id}-mode`), hint: doc.getElementById(`${id}-hint`) });
function defaultLabel(input, def) {
  const raw = input.dataset.defaultValue;
  if (raw == null || raw === '') return 'not reported';
  if (Number(raw) === 0) return def.offLabel || (def.unit === 'sessions' ? 'no session cap' : 'not reported');
  return `${raw}${def.unit === '%' ? '' : ' '}${def.unit === 'cores' && Number(raw) === 1 ? 'core' : def.unit}`;
}
function showPolicyValue(doc, id) {
  const { input, mode } = elements(doc, id);
  input.value = mode.value === 'inherit' && Number(input.dataset.defaultValue) > 0 ? input.dataset.defaultValue : '';
}
export function refreshDirectSetting(doc, id, { resetDraft = false } = {}) {
  const def = directSettings[id];
  const { input, mode, hint } = elements(doc, id);
  if (!input || !mode) return;
  const inherited = defaultLabel(input, def);
  const following = mode.value === 'inherit';
  const number = input.closest('.setting-number');
  number.dataset.policy = mode.value;
  // A native policy selector discloses the number only for a custom limit.
  if (mode.tagName === 'SELECT') {
    mode.disabled = input.disabled;
    number.hidden = mode.value !== 'custom';
  }
  if (resetDraft) {
    delete input.dataset.customValue;
    delete input.dataset.overrideMode;
    if (mode.value === 'custom') input.dataset.customValue = input.value;
    if (!following) input.dataset.overrideMode = mode.value;
  }
  input.readOnly = has(def, 'inherit') && mode.value !== 'custom';
  // Refresh effective values without turning inheritance into a fixed override.
  if (following) showPolicyValue(doc, id);
  const showUnit = mode.value !== 'off' && (!following || !!input.value);
  doc.getElementById(`${id}-unit`).style.visibility = showUnit ? '' : 'hidden';
  const description = [showUnit ? `${id}-unit` : '', `${id}-help`, `${id}-hint`, doc.getElementById(`${id}-capacity`) ? `${id}-capacity` : ''].filter(Boolean).join(' ');
  if (input.getAttribute('aria-describedby') !== description) input.setAttribute('aria-describedby', description);
  input.placeholder = mode.value === 'off' ? def.offLabel : mode.value === 'inherit' ? inherited : 'Enter a value';
  const message = mode.tagName === 'SELECT' && mode.value === 'off' ? (id === 'hibernate-custom-minutes' ? 'Idle replicas stay running until stopped or scaled down.' : 'No app limit. Available host resources are shared.') : following ? `Follows server default: ${inherited}.`
    : has(def, 'inherit') ? `Server default: ${inherited}.`
    : mode.value === 'off' ? 'Type a render cost to enable pacing.' : 'Using this app’s render cost.';
  if (hint.textContent !== message) hint.textContent = message;
  for (const control of doc.querySelectorAll(`[data-setting="${id}"]`)) {
    control.disabled = input.disabled || (following && control.dataset.policy === 'off');
    if (control.type === 'checkbox') {
      control.checked = control.dataset.policy === 'override' ? !following : mode.value === 'off';
    } else control.setAttribute('aria-pressed', String(control.dataset.policy === mode.value));
  }
}
export function populateDirectSetting(doc, id, value, canEdit) {
  const def = directSettings[id];
  const { input, mode } = elements(doc, id);
  mode.value = has(def, 'inherit') && (value == null || value === def.inherit) ? 'inherit'
    : has(def, 'off') && (value == null || value === def.off) ? 'off' : 'custom';
  input.removeAttribute('aria-invalid');
  input.disabled = !canEdit;
  if (mode.value === 'custom') input.value = String(Number((value * (def.factor || 1)).toFixed(6)));
  else showPolicyValue(doc, id);
  refreshDirectSetting(doc, id, { resetDraft: true });
}
export function installDirectSetting(doc, id, onChange) {
  const def = directSettings[id];
  const { input, mode } = elements(doc, id);
  function showCustomValue() {
    input.value = input.dataset.customValue ?? (Number(input.dataset.defaultValue) > 0 ? input.dataset.defaultValue : '');
  }
  if (mode.tagName === 'SELECT') mode.addEventListener('change', () => {
    if (mode.disabled || input.disabled) return;
    input.removeAttribute('aria-invalid');
    input.removeAttribute('aria-errormessage');
    if (mode.value === 'custom') showCustomValue();
    else showPolicyValue(doc, id);
    refreshDirectSetting(doc, id);
    onChange();
  });
  input.addEventListener('input', () => {
    if (input.disabled || input.readOnly) return;
    mode.value = 'custom';
    input.dataset.customValue = input.value;
    input.dataset.overrideMode = 'custom';
    input.removeAttribute('aria-invalid');
    refreshDirectSetting(doc, id);
    onChange();
  });
  for (const control of doc.querySelectorAll(`[data-setting="${id}"]`)) control.addEventListener(control.type === 'checkbox' ? 'change' : 'click', () => {
    if (input.disabled || control.disabled) return;
    if (mode.value === 'custom') input.dataset.customValue = input.value;
    if (control.dataset.policy === 'override') {
      if (!control.checked) {
        input.dataset.overrideMode = mode.value;
        mode.value = 'inherit';
      } else {
        mode.value = input.dataset.overrideMode || (has(def, 'off') && input.dataset.defaultValue === '0' ? 'off' : 'custom');
      }
    } else {
      mode.value = control.type === 'checkbox' && !control.checked ? 'custom' : 'off';
      input.dataset.overrideMode = mode.value;
    }
    input.removeAttribute('aria-invalid');
    if (mode.value === 'custom') showCustomValue();
    else showPolicyValue(doc, id);
    refreshDirectSetting(doc, id);
    onChange();
  });
}
export function readDirectSetting(doc, id) {
  const def = directSettings[id];
  const { input, mode } = elements(doc, id);
  if (mode.value === 'inherit' && has(def, 'inherit')) return def.inherit;
  if (mode.value === 'off' && has(def, 'off')) return def.off;
  const raw = input.value.trim();
  const value = Number(raw);
  if (!raw || !Number.isFinite(value) || (!def.decimal && !/^\d+$/.test(raw)) || value < def.min || value > def.max) {
    const range = def.min === Number.MIN_VALUE ? `greater than 0 and up to ${def.max}` : `from ${def.min} to ${def.max}`;
    const error = new Error(`${def.label}: enter ${def.decimal ? 'a number' : 'a whole number'} ${range} ${def.unit}.`);
    error.fieldId = id;
    throw error;
  }
  if (def.precision != null && Math.abs(value * 10 ** def.precision - Math.round(value * 10 ** def.precision)) > 1e-8) {
    const error = new Error(`${def.label}: use increments of 0.01 core.`);
    error.fieldId = id;
    throw error;
  }
  const stored = value / (def.factor || 1);
  return def.precision != null ? Math.round(stored) : stored;
}
