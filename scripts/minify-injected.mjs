#!/usr/bin/env node
// Minifies the scripts ShinyHub inlines into pages it did not write: the app
// switcher, the status overlay, the announcement banner, the support-session
// rail and the environment badge. Each ships as <name>.min.js beside its
// readable source. The Go packages embed the minified sibling and derive the
// CSP hash from those bytes, so what a browser runs is exactly what is in the
// tree, and a page load carries the minified size instead of the commented
// source.
//
// The siblings are committed rather than built on demand: the Go module must
// compile from a clean clone and from a module zip, and neither has Node. A
// source edit therefore needs a regenerate, and --check is how a forgotten one
// is caught: it minifies in memory and fails naming every sibling that differs.
//
//   node scripts/minify-injected.mjs             rewrite every sibling
//   node scripts/minify-injected.mjs --check     exit 1 naming stale siblings
//   node scripts/minify-injected.mjs <source>..  only these sources
//
// Not registered here: sessionui composes its script at run time from the
// dashboard's controller module, supportui's page.js is a few hundred bytes,
// and devreload.js only runs in development.
import { readFile, writeFile } from 'node:fs/promises';
import { relative, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { minify } from 'terser';

const root = fileURLToPath(new URL('..', import.meta.url));
const self = relative(root, fileURLToPath(import.meta.url));

// The one list of inline scripts. A Go package names its source in the
// go:generate directive beside the embed; this is where that source is
// registered so --check covers it.
const SOURCES = [
  'internal/appnav/assets/nav.js',
  'internal/proxy/assets/overlay.js',
  'internal/announcementui/assets/banner.js',
  'internal/supportui/assets/banner.js',
  'internal/envui/assets/environment.js',
];

// Every script is a self-contained block inlined into a third-party page:
// nothing it declares is reachable from outside and nothing outside may be
// assumed, so top-level names are left alone and only the scopes inside are
// mangled. The output is plain ASCII so the host page's charset cannot change
// a string. Comments go; the source keeps the explanations.
const OPTIONS = {
  ecma: 2020,
  module: false,
  toplevel: false,
  compress: { passes: 2 },
  mangle: true,
  format: { comments: false, ascii_only: true },
};

const siblingOf = (source) => source.replace(/\.js$/, '.min.js');

async function minified(source) {
  const code = await readFile(resolve(root, source), 'utf8');
  const out = await minify(code, OPTIONS);
  return out.code + '\n';
}

async function main(argv) {
  const check = argv.includes('--check');
  const requested = argv.filter((arg) => arg !== '--check').map((arg) => relative(root, resolve(arg)));
  for (const source of requested) {
    if (!SOURCES.includes(source)) {
      throw new Error(`${source} is not a registered inline script; add it to SOURCES in ${self}`);
    }
  }
  const sources = requested.length > 0 ? requested : SOURCES;

  const stale = [];
  for (const source of sources) {
    const sibling = siblingOf(source);
    const want = await minified(source);
    if (check) {
      const have = await readFile(resolve(root, sibling), 'utf8').catch(() => null);
      if (have !== want) stale.push(sibling);
      continue;
    }
    await writeFile(resolve(root, sibling), want);
    console.log(`${sibling}: ${Buffer.byteLength(want)} bytes`);
  }

  if (stale.length > 0) {
    console.error(`stale minified scripts (run make ui-minify):\n  ${stale.join('\n  ')}`);
    process.exit(1);
  }
  if (check) console.log(`${sources.length} minified scripts match their sources`);
}

await main(process.argv.slice(2));
