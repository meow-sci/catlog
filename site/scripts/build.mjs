#!/usr/bin/env node
/**
 * catlog site build (INITIAL_IMPL_PLAN §8).
 *
 *   assets/js/*.js   --esbuild bundle-->  dist/js/
 *   assets/css/      --copy---------->    dist/css/
 *   vendored deps    --copy---------->    dist/vendor/
 *
 * There is no dev server and no framework: the Go server renders every page and
 * serves dist/ at /static/ in dev (nginx serves it in prod).
 */

import { build } from "esbuild";
import { createRequire } from "node:module";
import { cp, mkdir, readdir, rm } from "node:fs/promises";
import path from "node:path";
import process from "node:process";
import { fileURLToPath } from "node:url";

const require = createRequire(import.meta.url);
const siteDir = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const assetsDir = path.join(siteDir, "assets");
const distDir = path.join(siteDir, "dist");

/**
 * Files copied verbatim into dist/vendor/. Resolved through node's resolver so a
 * version bump in package.json cannot silently leave a stale path behind.
 *
 * TODO(WP5): add the datastar browser bundle here. The npm package name under the
 * starfederation org must be resolved at `pnpm add` time and recorded in
 * docs/DECISIONS.md — do not guess it.
 */
const vendorFiles = [{ from: require.resolve("@picocss/pico/css/pico.min.css"), to: "pico.min.css" }];

/** @param {string} dir @returns {Promise<string[]>} entries, or [] when dir is absent */
async function listDir(dir) {
  try {
    return await readdir(dir);
  } catch (err) {
    if (err.code === "ENOENT") return [];
    throw err;
  }
}

async function main() {
  await rm(distDir, { recursive: true, force: true });
  await mkdir(distDir, { recursive: true });

  // --- js: one bundle per top-level module in assets/js/ ---
  const jsDir = path.join(assetsDir, "js");
  const entryPoints = (await listDir(jsDir))
    .filter((name) => name.endsWith(".js"))
    .map((name) => path.join(jsDir, name));

  if (entryPoints.length > 0) {
    await build({
      entryPoints,
      outdir: path.join(distDir, "js"),
      bundle: true,
      format: "esm",
      target: "es2022",
      minify: true,
      sourcemap: true,
      logLevel: "warning",
    });
  }
  console.log(`js:     ${entryPoints.length} bundle(s)`);

  // --- css: copied as-is; pico does the heavy lifting ---
  const cssDir = path.join(assetsDir, "css");
  const cssFiles = (await listDir(cssDir)).filter((name) => name.endsWith(".css"));
  if (cssFiles.length > 0) {
    await mkdir(path.join(distDir, "css"), { recursive: true });
    for (const name of cssFiles) {
      await cp(path.join(cssDir, name), path.join(distDir, "css", name));
    }
  }
  console.log(`css:    ${cssFiles.length} file(s)`);

  // --- vendor ---
  await mkdir(path.join(distDir, "vendor"), { recursive: true });
  for (const { from, to } of vendorFiles) {
    await cp(from, path.join(distDir, "vendor", to));
  }
  console.log(`vendor: ${vendorFiles.length} file(s)`);

  console.log(`built   ${path.relative(process.cwd(), distDir)}`);
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
