// Fleet read-only dashboard surface. Pure predicates + copy strings, plus
// thin DOM builders that take an explicit document/container (same contract
// as deploy-summary.js) so the whole module is unit-testable under jsdom.
//
// The dashboard is read-only for fleet: this module only describes ownership
// and the live deployment digest. It never asserts manifest conformance -
// that is what `shinyhub fleet plan` is for.

// An app is fleet-managed iff its managed_by is a non-empty string. This
// mirrors the CLI's buildFleetStatus predicate exactly: an empty string is
// unmanaged, not "managed by an empty fleet".
export function isFleetManaged(app) {
  return !!app && typeof app.managed_by === 'string' && app.managed_by.length > 0;
}

// managed_by already carries the "fleet:<id>" form (stamped by fleet apply),
// so the badge reads "managed by fleet:<id>".
export function fleetBadgeText(app) {
  return isFleetManaged(app) ? `managed by ${app.managed_by}` : null;
}

// Card label. Deliberately a constant, not the fleet id: the card only needs
// to say "this app is governed by a manifest, do not hand-edit it", and WHICH
// fleet is detail-page material. A fixed label also makes card overflow
// structurally impossible, where an operator-chosen id is unbounded. The id
// stays one hover away in fleetBadgeTooltip.
export const FLEET_BADGE_COMPACT_LABEL = 'fleet';

export const FLEET_BADGE_TOOLTIP =
  'Configuration for this app is governed by a fleet manifest. ' +
  'UI/CLI changes to fleet-declared fields will be reverted on the next ' +
  '`fleet apply`. ' +
  'Run `shinyhub fleet plan -f <your-manifest>` to check this app against ' +
  'its manifest.';

// Tooltip for the compact badge. The card label omits the id entirely, so the
// tooltip is the only place it appears on the dashboard and must lead with it.
export function fleetBadgeTooltip(app) {
  const full = fleetBadgeText(app);
  return full ? `${full}. ${FLEET_BADGE_TOOLTIP}` : FLEET_BADGE_TOOLTIP;
}

const DIGEST_PREFIX = 'sha256:';
const DIGEST_SHORT_LEN = 12;

// Short, display-only form of a content digest. Returns null for an absent
// or empty digest so callers can branch on a single value.
export function shortContentDigest(digest) {
  if (typeof digest !== 'string' || digest.length === 0) return null;
  const bare = digest.startsWith(DIGEST_PREFIX)
    ? digest.slice(DIGEST_PREFIX.length)
    : digest;
  return bare.slice(0, DIGEST_SHORT_LEN) || null;
}

export const DIGEST_LABEL = 'live deployment digest';

export const DIGEST_DISCLAIMER =
  'This is the digest of the live deployment, not a manifest-conformance ' +
  'signal. Run `shinyhub fleet plan -f <your-manifest>` to check this app ' +
  'against its manifest.';

// Grid segment filter. Unknown segment falls back to "all". Always returns a
// fresh array so callers never mutate state.apps through it.
export function segmentApps(apps, segment) {
  const list = Array.isArray(apps) ? apps : [];
  if (segment === 'fleet') return list.filter(isFleetManaged);
  if (segment === 'unmanaged') return list.filter((a) => !isFleetManaged(a));
  return list.slice();
}

// Builds a fleet ownership badge for managed apps; null otherwise so callers
// can do `const b = makeFleetBadge(...); if (b) parent.appendChild(b);`.
//
// opts.compact selects the card form: the fixed "fleet" label plus the full
// ownership sentence in the tooltip. The uncompacted form is the detail header,
// which has room for the id and shows it inline.
export function makeFleetBadge(doc, app, opts = {}) {
  if (!isFleetManaged(app)) return null;
  const compact = opts.compact === true;
  const badge = doc.createElement('span');
  badge.className = 'badge badge-fleet';
  badge.textContent = compact ? FLEET_BADGE_COMPACT_LABEL : fleetBadgeText(app);
  badge.title = compact ? fleetBadgeTooltip(app) : FLEET_BADGE_TOOLTIP;
  return badge;
}

// Fills `container` with the live deployment digest line and reveals it, or
// clears + hides it when the app is unmanaged or has no digest. Mirrors the
// renderDeployResult contract: caller owns the container, we own its content
// and hidden state.
export function renderFleetDigest(container, app) {
  container.textContent = '';
  const short = shortContentDigest(app && app.content_digest);
  if (!isFleetManaged(app) || short === null) {
    container.hidden = true;
    return;
  }
  const doc = container.ownerDocument;

  const label = doc.createElement('span');
  label.className = 'fleet-digest-label';
  label.textContent = DIGEST_LABEL;

  const code = doc.createElement('code');
  code.className = 'fleet-digest-value';
  code.textContent = short;

  const when = doc.createElement('span');
  when.className = 'fleet-digest-when';
  when.textContent = app.last_deployed_at
    ? new Date(app.last_deployed_at).toLocaleString()
    : '—';

  const note = doc.createElement('p');
  note.className = 'fleet-digest-note';
  note.textContent = DIGEST_DISCLAIMER;

  container.append(label, code, when, note);
  container.hidden = false;
}
