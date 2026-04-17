// Minimal ZIP writer using the browser's native DEFLATE (CompressionStream).
// Produces the same wire format as Go's archive/zip — local file headers,
// central directory records, EOCD — with the UTF-8 name flag set so non-ASCII
// paths round-trip safely. Shared between app.js (browser) and the Node test
// harness (internal/ui/testdata/run-zip-writer.mjs). No DOM dependencies.
(function (root) {
  'use strict';

  // CRC-32 (IEEE 802.3 polynomial, reflected).
  const CRC32_TABLE = (() => {
    const t = new Uint32Array(256);
    for (let i = 0; i < 256; i++) {
      let c = i;
      for (let k = 0; k < 8; k++) c = (c & 1) ? (0xEDB88320 ^ (c >>> 1)) : (c >>> 1);
      t[i] = c;
    }
    return t;
  })();

  function crc32(bytes) {
    let c = 0xFFFFFFFF;
    for (let i = 0; i < bytes.length; i++) c = CRC32_TABLE[(c ^ bytes[i]) & 0xFF] ^ (c >>> 8);
    return (c ^ 0xFFFFFFFF) >>> 0;
  }

  let compressionStreamSupport = null;
  function supportsCompressionStream() {
    if (compressionStreamSupport !== null) return compressionStreamSupport;
    if (typeof CompressionStream === 'undefined') { compressionStreamSupport = false; return false; }
    try { new CompressionStream('deflate-raw'); compressionStreamSupport = true; }
    catch { compressionStreamSupport = false; }
    return compressionStreamSupport;
  }

  async function deflateRaw(bytes) {
    const stream = new Blob([bytes]).stream().pipeThrough(new CompressionStream('deflate-raw'));
    return new Uint8Array(await new Response(stream).arrayBuffer());
  }

  function dosDateTime(d) {
    const time = ((d.getHours() & 0x1F) << 11) | ((d.getMinutes() & 0x3F) << 5) | ((d.getSeconds() >> 1) & 0x1F);
    const date = (((d.getFullYear() - 1980) & 0x7F) << 9) | (((d.getMonth() + 1) & 0x0F) << 5) | (d.getDate() & 0x1F);
    return { time, date };
  }

  async function buildZipNative(fileList) {
    const { time, date } = dosDateTime(new Date());
    const encoder = new TextEncoder();
    const parts = [];
    const records = [];
    let offset = 0;

    for (const { relativePath, file } of fileList) {
      const uncompressed = new Uint8Array(await file.arrayBuffer());
      const uncompressedSize = uncompressed.length;
      const checksum = crc32(uncompressed);
      const compressed = await deflateRaw(uncompressed);
      const compressedSize = compressed.length;
      const nameBytes = encoder.encode(relativePath);

      const lfh = new Uint8Array(30 + nameBytes.length);
      const lv = new DataView(lfh.buffer);
      lv.setUint32(0, 0x04034b50, true);
      lv.setUint16(4, 20, true);
      lv.setUint16(6, 0x0800, true);
      lv.setUint16(8, 8, true);
      lv.setUint16(10, time, true);
      lv.setUint16(12, date, true);
      lv.setUint32(14, checksum, true);
      lv.setUint32(18, compressedSize, true);
      lv.setUint32(22, uncompressedSize, true);
      lv.setUint16(26, nameBytes.length, true);
      lv.setUint16(28, 0, true);
      lfh.set(nameBytes, 30);

      parts.push(lfh, compressed);
      records.push({ nameBytes, checksum, compressedSize, uncompressedSize, localHeaderOffset: offset });
      offset += lfh.length + compressedSize;
    }

    const cdStart = offset;
    for (const r of records) {
      const cdr = new Uint8Array(46 + r.nameBytes.length);
      const cv = new DataView(cdr.buffer);
      cv.setUint32(0, 0x02014b50, true);
      cv.setUint16(4, 20, true);
      cv.setUint16(6, 20, true);
      cv.setUint16(8, 0x0800, true);
      cv.setUint16(10, 8, true);
      cv.setUint16(12, time, true);
      cv.setUint16(14, date, true);
      cv.setUint32(16, r.checksum, true);
      cv.setUint32(20, r.compressedSize, true);
      cv.setUint32(24, r.uncompressedSize, true);
      cv.setUint16(28, r.nameBytes.length, true);
      cv.setUint16(30, 0, true);
      cv.setUint16(32, 0, true);
      cv.setUint16(34, 0, true);
      cv.setUint16(36, 0, true);
      cv.setUint32(38, 0, true);
      cv.setUint32(42, r.localHeaderOffset, true);
      cdr.set(r.nameBytes, 46);
      parts.push(cdr);
      offset += cdr.length;
    }
    const cdSize = offset - cdStart;

    const eocd = new Uint8Array(22);
    const ev = new DataView(eocd.buffer);
    ev.setUint32(0, 0x06054b50, true);
    ev.setUint16(4, 0, true);
    ev.setUint16(6, 0, true);
    ev.setUint16(8, records.length, true);
    ev.setUint16(10, records.length, true);
    ev.setUint32(12, cdSize, true);
    ev.setUint32(16, cdStart, true);
    ev.setUint16(20, 0, true);
    parts.push(eocd);

    return new Blob(parts, { type: 'application/zip' });
  }

  root.buildZipNative = buildZipNative;
  root.supportsCompressionStream = supportsCompressionStream;
})(typeof globalThis !== 'undefined' ? globalThis : window);
