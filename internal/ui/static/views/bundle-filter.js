// Client-side mirror of internal/bundle.Rules.Inspect, used when the browser
// zips a dropped folder. It must classify identically to the server, or the
// browser uploads bytes the server would have refused and the two deploy paths
// disagree about what a bundle contains.

// inspectBundleEntry classifies one entry relative to the bundle root and
// returns one of: 'accept', 'skipCacheDir', 'rejectDataDir', 'rejectDatasetDir',
// 'rejectExtension', 'rejectFileSize'. Pass size 0 for a directory.
//
// The leading-slash strip keeps classification in lockstep with the server,
// which operates on paths cleaned via path.Clean.
export function inspectBundleEntry(rules, relPath, size) {
  const clean = String(relPath || '').replace(/^\/+/, '');
  const first = clean.split('/')[0];
  if (first === 'data') return 'rejectDataDir';
  if (first === 'datasets' || first === '.shinyhub-data') return 'rejectDatasetDir';
  if (matchesCacheDir(rules.cacheDirs, clean, first)) return 'skipCacheDir';
  const lower = clean.toLowerCase();
  for (const ext of rules.dataExtensions || []) {
    if (lower.endsWith(ext.toLowerCase())) return 'rejectExtension';
  }
  if (rules.maxFileBytes > 0 && size > rules.maxFileBytes) return 'rejectFileSize';
  return 'accept';
}

// matchesCacheDir applies the two shapes a cacheDirs entry can take. A bare
// name matches the first path segment; a name containing a slash
// ("renv/library") matches that whole prefix, because the directory it names
// does not sit at the bundle root. Matching a slash-bearing entry against the
// first segment alone would never fire, so the browser would upload a cache
// the CLI excludes.
function matchesCacheDir(cacheDirs, clean, first) {
  for (const c of cacheDirs || []) {
    if (c.includes('/')) {
      if (clean === c || clean.startsWith(c + '/')) return true;
      continue;
    }
    if (first === c) return true;
  }
  return false;
}
