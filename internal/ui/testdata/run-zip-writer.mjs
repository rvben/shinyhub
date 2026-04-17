// Node harness for internal/ui/zip_writer_test.go.
//
// Reads a JSON manifest from stdin:
//   [{ "path": "app.py", "body_base64": "..." }, ...]
// Evaluates the browser-shared zip-writer.js to populate globalThis, then
// calls buildZipNative and writes the resulting ZIP bytes to stdout.
//
// Requires Node >= 18 (CompressionStream built-in).
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import path from 'node:path';

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);

const writerSrc = readFileSync(path.join(__dirname, '..', 'static', 'zip-writer.js'), 'utf8');
// Indirect eval runs in global scope; the IIFE attaches buildZipNative to globalThis.
(0, eval)(writerSrc);

let manifestJSON = '';
for await (const chunk of process.stdin) manifestJSON += chunk;
const manifest = JSON.parse(manifestJSON);

const fileList = manifest.map(({ path, body_base64 }) => ({
  relativePath: path,
  file: new Blob([Buffer.from(body_base64, 'base64')]),
}));

const blob = await globalThis.buildZipNative(fileList);
const buf = Buffer.from(await blob.arrayBuffer());
process.stdout.write(buf);
