// Pure helpers for the sidebar identity card and the profile modal. DOM-free so
// the initials, avatar-hue, and display-name-fallback logic stay unit-testable;
// app.js imports these and owns the DOM wiring. Mirrors the codebase's
// testable-leaf convention (app-card-badge.js, sidebar-nav.js).

function firstAlnum(word) {
  const m = String(word).match(/[a-zA-Z0-9]/);
  return m ? m[0] : '';
}

// deriveInitials returns 1-2 uppercase letters for a single source string, or
// '' when the source has no usable latin alphanumerics. A multi-word value uses
// the first letter of its first and last words; a single token uses its first
// two alphanumerics.
function deriveInitials(value) {
  const s = String(value || '').trim();
  if (!s) return '';
  const words = s.split(/\s+/).filter(Boolean);
  if (words.length >= 2) {
    return (firstAlnum(words[0]) + firstAlnum(words[words.length - 1])).toUpperCase();
  }
  const alnum = s.replace(/[^a-zA-Z0-9]/g, '');
  return alnum.slice(0, 2).toUpperCase();
}

// initials picks the avatar's 1-2 letters, preferring the display name, then the
// username, then '?' so the avatar is never blank.
export function initials(displayName, username) {
  return deriveInitials(displayName) || deriveInitials(username) || '?';
}

// avatarHue maps a seed string to a stable hue in [0,360). Deterministic so a
// given user keeps the same avatar color across sessions and reloads.
export function avatarHue(seed) {
  const s = String(seed || '');
  let h = 0;
  for (let i = 0; i < s.length; i++) {
    h = (h * 31 + s.charCodeAt(i)) >>> 0;
  }
  return h % 360;
}

// identityModel builds the view model for the sidebar identity card and the
// profile header from a session user object. The name falls back to the
// username when no display name is set; the username appears as a secondary
// line only when a display name is present (otherwise the primary line already
// IS the username). The avatar hue is seeded by username so the color stays
// stable even after the user edits their display name.
export function identityModel(user) {
  const u = user || {};
  const username = String(u.username || '').trim();
  const display = String(u.display_name || '').trim();
  const role = String(u.role || '').trim();
  const name = display || username || 'Unknown';
  return {
    name,
    username,
    role,
    roleLabel: role ? role.charAt(0).toUpperCase() + role.slice(1) : '',
    secondary: display && username ? username : '',
    initials: initials(display, username),
    hue: avatarHue(username || display || name),
  };
}
