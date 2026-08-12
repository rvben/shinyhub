export const CLI_CONNECT_STORAGE_KEY = 'pendingCLIConnect';

const HASH_RE = /^[a-f0-9]{64}$/;
const CODE_RE = /^[A-F0-9]{4}-[A-F0-9]{4}$/;

// Parse only the bounded values the terminal generated. Invalid or partial
// URLs are ordinary token-page visits, not malformed approval requests.
export function cliConnectRequestFromSearch(search = '') {
  const params = new URLSearchParams(search);
  const tokenHash = params.get('connect_hash') || '';
  const name = params.get('connect_name') || '';
  const code = params.get('connect_code') || '';
  if (!HASH_RE.test(tokenHash) || !name || name.length > 64 || !CODE_RE.test(code)) return null;
  return { tokenHash, name, code };
}

export function validCLIConnectRequest(value) {
  return !!value && HASH_RE.test(value.tokenHash || '') &&
    typeof value.name === 'string' && value.name.length > 0 && value.name.length <= 64 &&
    CODE_RE.test(value.code || '');
}

export function cliConnectDeviceLabel(name) {
  const label = String(name || '').replace(/^cli-/, '').replace(/-[a-f0-9]{6}$/, '');
  return label || 'terminal';
}
