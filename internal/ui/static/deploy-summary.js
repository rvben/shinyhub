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
