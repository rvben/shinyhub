// Pure envelope-unwrap for GET /api/apps/:slug. The server returns a wrapped
// object (see internal/api/apps.go handleGetApp):
//   { app, replicas_status, can_manage, runtime_mode, resource_enforcement,
//     release_number, released_at, released_version }
// normalizeAppEnvelope folds the envelope-level fields back onto the app object
// so the view reads app.* consistently, and returns the replicas_status array.
//
// Kept DOM-free and unit-tested (jstests/app-detail-envelope.test.js) because a
// wrong unwrap makes every field undefined and silently breaks the detail page —
// the wrapped-response regression class the contract tests guard against.
export function normalizeAppEnvelope(body) {
  const app = body.app || body;

  // can_manage tells the view whether this visitor manages the app (drives the
  // manager-only tabs + header kebab); only trust a real boolean.
  if (typeof body.can_manage === 'boolean') app.can_manage = body.can_manage;

  // runtime_mode + resource_enforcement drive the Resources controls: limits
  // apply in both native and docker mode, and resource_enforcement {memory,cpu}
  // says whether native cgroup enforcement is actually active.
  if (typeof body.runtime_mode === 'string') app.runtime_mode = body.runtime_mode;
  if (body.resource_enforcement && typeof body.resource_enforcement === 'object') {
    app.resource_enforcement = body.resource_enforcement;
  }

  // release_number/released_at (human-friendly "vN · date") live at the envelope
  // level. Absent until the app has a succeeded deploy — normalize to null so the
  // header hides the version chip / deployed-ago meta rather than showing stale
  // values from a prior app.
  app.release_number = (typeof body.release_number === 'number') ? body.release_number : null;
  app.released_at = body.released_at || null;
  // The live succeeded bundle's epoch id (for the "bundle …" hover) — distinct
  // from current_version, which is the newest row regardless of status.
  app.released_version = body.released_version || null;

  const replicasStatus = Array.isArray(body.replicas_status) ? body.replicas_status : [];
  return { app, replicasStatus };
}
