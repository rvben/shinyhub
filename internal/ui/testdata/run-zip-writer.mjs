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
import vm from 'node:vm';

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);

const writerPath = path.join(__dirname, '..', 'static', 'zip-writer.js');
// Runs the script in this process's global scope, as a browser would; the IIFE
// attaches buildZipNative to globalThis.
vm.runInThisContext(readFileSync(writerPath, 'utf8'), { filename: writerPath });

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
