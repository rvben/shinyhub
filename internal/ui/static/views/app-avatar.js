// app-avatar.js - the single source of truth for an app's visual identity: an
// uploaded icon when one is set, otherwise a deterministic monogram. Shared by
// the Launchpad tile, the app-detail header, and the Configuration icon picker
// so all three render the same thing. The model half is DOM-free (unit-tested);
// renderAppAvatar takes an explicit document so it stays testable too.

// appAvatar derives a deterministic monogram: 1-2 initials from the name and a
// hue from the slug. The view renders it in OKLCH at a fixed lightness/chroma so
// every monogram is colorful yet cohesive on the dark theme.
export function appAvatar(app) {
  const name = ((app && (app.name || app.slug)) || '?').trim();
  const words = name.split(/[\s_-]+/).filter(Boolean);
  let initials = words.slice(0, 2).map((w) => w[0]).join('');
  if (!initials) initials = name.slice(0, 2) || '?';
  let h = 2166136261;
  const seed = (app && (app.slug || app.name)) || name;
  for (let i = 0; i < seed.length; i++) {
    h ^= seed.charCodeAt(i);
    h = Math.imul(h, 16777619);
  }
  return { initials: initials.slice(0, 2).toUpperCase(), hue: (h >>> 0) % 360 };
}

// appIconUrl returns the URL of the app's uploaded icon, or '' when none is set
// (icon_mime empty). updated_at is appended as a cache-buster so a replaced icon
// shows immediately instead of serving a stale cached copy.
export function appIconUrl(app) {
  if (!app || !app.icon_mime || !app.slug) return '';
  const v = app.updated_at ? `?v=${encodeURIComponent(app.updated_at)}` : '';
  return `/api/apps/${encodeURIComponent(app.slug)}/icon${v}`;
}

// avatarView normalizes a raw app into the shape renderAppAvatar consumes.
export function avatarView(app) {
  const mono = appAvatar(app);
  return { iconUrl: appIconUrl(app), initials: mono.initials, hue: mono.hue };
}

// renderAppAvatar returns a DOM node for the app's identity: an <img> of the
// uploaded icon (with a monogram fallback if the image 404s/errors), or the
// monogram directly. baseClass styles the box; the icon variant also gets
// `${baseClass}--img` so CSS can drop the monogram background/letter styling.
// view is {iconUrl, initials, hue} (use avatarView(app) to build it).
export function renderAppAvatar(doc, view, baseClass) {
  if (view && view.iconUrl) {
    const img = doc.createElement('img');
    img.className = `${baseClass} ${baseClass}--img`;
    img.src = view.iconUrl;
    img.alt = '';
    img.setAttribute('aria-hidden', 'true');
    img.loading = 'lazy';
    // If the icon fails to load, swap in the monogram in place so a broken or
    // deleted image never leaves an empty box.
    img.addEventListener('error', () => img.replaceWith(monogram(doc, view, baseClass)), { once: true });
    return img;
  }
  return monogram(doc, view || {}, baseClass);
}

function monogram(doc, view, baseClass) {
  const span = doc.createElement('span');
  span.className = baseClass;
  span.textContent = view.initials || '?';
  if (view.hue != null) span.style.setProperty('--avatar-hue', String(view.hue));
  span.setAttribute('aria-hidden', 'true');
  return span;
}
