#!/usr/bin/env node
/**
 * Pre-compress a built asset tree in place: `foo.css` gains `foo.css.br` and
 * `foo.css.gz` beside it.
 *
 *   node scripts/precompress.mjs site/dist spa/dist
 *
 * nginx serves the siblings with `brotli_static on; gzip_static on;` — see
 * infra/nginx/prod.conf.tmpl — so the origin never compresses a static asset at
 * request time, and the bytes it does send were compressed at a quality no
 * on-the-fly setting would pay for (brotli 11 costs ~100x brotli 5 in CPU and
 * is free here, because it happens once, at build time, in a container stage
 * that is thrown away).
 *
 * No dependency, deliberately: node's own zlib has brotli, and both frontends
 * are hermetic by design (no CDN, no runtime fetch). A compressor from npm
 * would be the only build input neither `site/` nor `spa/` needs for anything
 * else.
 *
 * Idempotent: re-running overwrites the siblings and never compresses a
 * sibling. `--check` reports what would change and writes nothing.
 */

import { constants, brotliCompressSync, gzipSync } from 'node:zlib';
import { readdir, readFile, stat, unlink, writeFile } from 'node:fs/promises';
import path from 'node:path';
import process from 'node:process';

/**
 * Extensions worth compressing. Everything here is text or text-like; anything
 * not listed is either already compressed or too small to matter.
 */
const COMPRESSIBLE = new Set([
  '.html',
  '.css',
  '.js',
  '.mjs',
  '.map',
  '.json',
  '.svg',
  '.txt',
  '.xml',
  '.webmanifest',
]);

/**
 * The floor. Below this, the compressed sibling is usually *larger* than the
 * original once its header is counted, and even when it is not, the saving is
 * smaller than one TCP segment — nginx would spend a stat(2) per request to
 * find a file that saves nothing.
 */
const MIN_BYTES = 1024;

/**
 * woff2 is brotli-compressed internally (that is what distinguishes it from
 * woff), so `inter-latin-wght-normal.woff2.br` would be bytes spent to make the
 * file slightly bigger. It is not in COMPRESSIBLE, and this comment is here so
 * nobody adds it.
 */

/** @param {number} n @returns {string} */
const kb = (n) => `${(n / 1024).toFixed(1)}k`;

/**
 * Walks `dir` and yields every file path under it.
 *
 * @param {string} dir
 * @returns {AsyncGenerator<string>}
 */
async function* walk(dir) {
  const entries = await readdir(dir, { withFileTypes: true });
  for (const entry of entries) {
    const full = path.join(dir, entry.name);
    if (entry.isDirectory()) {
      yield* walk(full);
    } else if (entry.isFile()) {
      yield full;
    }
  }
}

/**
 * @param {Buffer} buf
 * @returns {Buffer}
 */
function brotli(buf) {
  return brotliCompressSync(buf, {
    params: {
      [constants.BROTLI_PARAM_QUALITY]: constants.BROTLI_MAX_QUALITY, // 11
      [constants.BROTLI_PARAM_LGWIN]: 24,
      [constants.BROTLI_PARAM_SIZE_HINT]: buf.length,
    },
  });
}

/**
 * Compresses one tree in place.
 *
 * @param {string} dir
 * @param {{check: boolean}} opts
 * @returns {Promise<{files: number, raw: number, br: number, gz: number, rows: string[][]}>}
 */
async function compressTree(dir, opts) {
  const totals = { files: 0, raw: 0, br: 0, gz: 0, rows: /** @type {string[][]} */ ([]) };

  for await (const file of walk(dir)) {
    const ext = path.extname(file);
    if (!COMPRESSIBLE.has(ext)) continue;

    const info = await stat(file);
    if (info.size < MIN_BYTES) continue;

    const buf = await readFile(file);
    const brBuf = brotli(buf);
    const gzBuf = gzipSync(buf, { level: 9 });

    // A sibling that is not smaller is a sibling nginx would serve to make the
    // response bigger. Drop it — and drop any stale one left by an earlier run
    // over different content, or nginx would keep serving that instead.
    const write = async (/** @type {string} */ suffix, /** @type {Buffer} */ out) => {
      const dest = file + suffix;
      if (out.length >= buf.length) {
        if (!opts.check) await unlink(dest).catch(() => {});
        return 0;
      }
      if (!opts.check) await writeFile(dest, out);
      return out.length;
    };

    const brBytes = await write('.br', brBuf);
    const gzBytes = await write('.gz', gzBuf);

    totals.files += 1;
    totals.raw += buf.length;
    totals.br += brBytes || buf.length;
    totals.gz += gzBytes || buf.length;
    totals.rows.push([
      path.relative(dir, file),
      kb(buf.length),
      brBytes ? kb(brBytes) : '—',
      gzBytes ? kb(gzBytes) : '—',
      brBytes ? `${((1 - brBytes / buf.length) * 100).toFixed(0)}%` : '—',
    ]);
  }

  return totals;
}

async function main() {
  const args = process.argv.slice(2);
  const check = args.includes('--check');
  const dirs = args.filter((a) => !a.startsWith('-'));

  if (dirs.length === 0) {
    console.error('usage: node scripts/precompress.mjs [--check] <dir> [dir…]');
    process.exit(2);
  }

  let grandRaw = 0;
  let grandBr = 0;

  for (const dir of dirs) {
    try {
      await stat(dir);
    } catch {
      // A missing tree is a build that did not run, not a tree with nothing in
      // it. Failing here beats shipping an image with no assets in it.
      console.error(`precompress: ${dir} does not exist — build it first`);
      process.exit(1);
    }

    const { files, raw, br, gz, rows } = await compressTree(dir, { check });
    grandRaw += raw;
    grandBr += br;

    console.log(`\n${dir}${check ? '  (--check: nothing written)' : ''}`);
    if (files === 0) {
      console.log('  nothing compressible above the 1 KiB floor');
      continue;
    }
    const width = Math.max(...rows.map((r) => r[0].length));
    for (const [name, rawKb, brKb, gzKb, saved] of rows) {
      console.log(`  ${name.padEnd(width)}  ${rawKb.padStart(8)} → br ${brKb.padStart(8)}  gz ${gzKb.padStart(8)}  ${saved.padStart(4)}`);
    }
    console.log(`  ${files} file(s): ${kb(raw)} → br ${kb(br)} / gz ${kb(gz)}`);
  }

  if (grandRaw > 0) {
    console.log(`\ntotal: ${kb(grandRaw)} → ${kb(grandBr)} brotli (${((1 - grandBr / grandRaw) * 100).toFixed(0)}% saved)`);
  }
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
