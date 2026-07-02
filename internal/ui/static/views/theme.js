// Theme preference + resolution. The dashboard ships a dark and a light palette
// (see style.css :root and :root[data-theme="light"]). The user picks one of
// three preferences - 'system' (follow the OS), 'light', or 'dark' - persisted in
// localStorage; 'system' tracks prefers-color-scheme live.
//
// The pure resolveTheme() is unit-tested (jstests/theme.test.js); the DOM/window
// wiring is pinned by contract_test.go. A tiny inline script in index.html applies
// the resolved theme before first paint (no flash); this module re-applies on load
// and keeps it in sync when the preference or the OS setting changes.

export const THEME_STORAGE_KEY = 'shinyhub-theme';
export const THEME_PREFERENCES = ['system', 'light', 'dark'];

// resolveTheme maps a stored preference + the OS "prefers light" signal to the
// effective palette ('light' | 'dark'). An unknown/absent preference is treated
// as 'system'.
export function resolveTheme(preference, prefersLight) {
  const pref = THEME_PREFERENCES.includes(preference) ? preference : 'system';
  if (pref === 'system') return prefersLight ? 'light' : 'dark';
  return pref;
}

// getThemePreference reads the stored preference, defaulting to 'system'. Tolerant
// of storage being unavailable (private mode / disabled) or holding a stale value.
export function getThemePreference(win) {
  try {
    const raw = win.localStorage.getItem(THEME_STORAGE_KEY);
    return THEME_PREFERENCES.includes(raw) ? raw : 'system';
  } catch {
    return 'system';
  }
}

function prefersLight(win) {
  try {
    return !!(win.matchMedia && win.matchMedia('(prefers-color-scheme: light)').matches);
  } catch {
    return false;
  }
}

// applyTheme stamps the effective theme onto <html data-theme>, which the CSS
// palette keys off. Returns the effective theme for callers that want it.
export function applyTheme(win, preference) {
  const effective = resolveTheme(preference, prefersLight(win));
  win.document.documentElement.dataset.theme = effective;
  return effective;
}

// setThemePreference persists the choice and applies it immediately.
export function setThemePreference(win, preference) {
  const pref = THEME_PREFERENCES.includes(preference) ? preference : 'system';
  try {
    win.localStorage.setItem(THEME_STORAGE_KEY, pref);
  } catch {
    /* storage unavailable: still apply for this session */
  }
  return applyTheme(win, pref);
}

// initTheme applies the stored preference and, while the preference is 'system',
// tracks OS light/dark changes live. Call once at startup. onChange(effective) is
// invoked whenever the effective theme changes so the UI (e.g. the profile
// control) can reflect it.
export function initTheme(win, onChange) {
  applyTheme(win, getThemePreference(win));
  try {
    const mq = win.matchMedia('(prefers-color-scheme: light)');
    const handler = () => {
      if (getThemePreference(win) === 'system') {
        const eff = applyTheme(win, 'system');
        if (onChange) onChange(eff);
      }
    };
    if (mq.addEventListener) mq.addEventListener('change', handler);
    else if (mq.addListener) mq.addListener(handler); // older browsers
  } catch {
    /* matchMedia unavailable: static theme is fine */
  }
}
