#!/usr/bin/env node
import { createFixture } from './acceptance/fixture.mjs';
import { runAPIJourneys, runLifecycleJourneys } from './acceptance/api.mjs';
import { runBrowserJourneys } from './acceptance/browser.mjs';

const args = process.argv.slice(2);
if (args.length && (args.length !== 1 || args[0] !== '--api-only')) throw new Error('Usage: acceptance-e2e.mjs [--api-only]');
const fixture = await createFixture();
try {
  await runAPIJourneys(fixture);
  if (!args.includes('--api-only')) await runBrowserJourneys(fixture);
  await runLifecycleJourneys(fixture);
  fixture.report.scope = args.includes('--api-only') ? 'api-only' : 'api-and-browser';
  fixture.report.status = 'passed';
} catch (error) {
  fixture.report.error = error.message;
  console.error(error);
  process.exitCode = 1;
} finally { await fixture.close(); }
