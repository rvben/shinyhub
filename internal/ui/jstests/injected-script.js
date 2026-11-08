// The scripts ShinyHub injects into app pages ship as minified siblings of
// their readable sources (scripts/minify-injected.mjs), and the Go packages
// embed the sibling. The jsdom tests evaluate the readable source by default,
// which is what a developer edits and debugs; make test-js then runs the same
// tests a second time with SHINYHUB_INJECTED_SCRIPTS=min, so the bytes that
// are actually embedded are proven to behave identically. Assertions about the
// source TEXT (a CSS rule, a copied constant) must keep reading the source:
// minification may rewrite text, never behaviour.
import { readFileSync } from 'node:fs';

export const injectedVariant = process.env.SHINYHUB_INJECTED_SCRIPTS === 'min' ? 'min' : 'source';

// injectedScript returns the script to evaluate for the given source URL: the
// source itself, or its .min.js sibling when the minified variant is selected.
export function injectedScript(sourceURL) {
  const source = String(sourceURL);
  if (!source.endsWith('.js') || source.endsWith('.min.js')) {
    throw new Error(`${source} is not a readable injected-script source`);
  }
  const url = injectedVariant === 'min' ? new URL(source.replace(/\.js$/, '.min.js')) : new URL(source);
  const code = readFileSync(url, 'utf8');
  if (code.length === 0) {
    throw new Error(`${url.pathname} is empty; regenerate with make ui-minify`);
  }
  return code;
}
