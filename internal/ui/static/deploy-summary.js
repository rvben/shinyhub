// formatManifestSummary mirrors internal/cli/manifest_summary.go: it turns
// the deploy response's body.manifest into ready-to-render lines. Returns
// an empty array when no manifest was applied so callers can decide
// whether to show the result block at all.
export function formatManifestSummary(manifest) {
  if (!manifest || typeof manifest !== 'object') return [];
  const lines = [];
  if (manifest.app && typeof manifest.app === 'object') {
    const keys = Object.keys(manifest.app).sort();
    if (keys.length > 0) {
      const parts = keys.map((k) => {
        const v = manifest.app[k];
        if (v === null) return `${k}=default`;
        if (k === 'autoscale' && v && typeof v === 'object') {
          return `autoscale=${formatAutoscaleSummary(v)}`;
        }
        return `${k}=${v}`;
      });
      lines.push(`Applied [app] settings: ${parts.join('; ')}`);
    }
  }
  if (Array.isArray(manifest.schedules) && manifest.schedules.length > 0) {
    let created = 0, updated = 0;
    for (const s of manifest.schedules) {
      if (s && s.action === 'created') created++;
      else if (s && s.action === 'updated') updated++;
    }
    lines.push(`Schedules: ${created} created, ${updated} updated`);
  }
  return lines;
}

// formatAutoscaleSummary mirrors formatAutoscaleSummary in
// internal/cli/manifest_summary.go: "off" when disabled, else
// "on (min-max @ target)" with target as a two-decimal fraction, or
// "@ default" when target is 0 (inherit the runtime default).
function formatAutoscaleSummary(as) {
  if (!as.enabled) return 'off';
  const min = Number(as.min_replicas) || 0;
  const max = Number(as.max_replicas) || 0;
  const target = Number(as.target) || 0;
  if (target > 0) return `on (${min}-${max} @ ${target.toFixed(2)})`;
  return `on (${min}-${max} @ default)`;
}

// renderDeployResult populates the result block with one <li> per line and
// reveals it. DOM refs are passed in so the same module is reusable from a
// JSDOM test without leaking globals.
export function renderDeployResult(container, list, lines) {
  list.innerHTML = '';
  for (const line of lines) {
    const li = list.ownerDocument.createElement('li');
    li.textContent = line;
    list.appendChild(li);
  }
  container.hidden = false;
}
