#!/usr/bin/env node
/**
 * catlog site build (§8).
 *
 *   assets/js/*.js   --esbuild bundle-->  dist/js/
 *   assets/css/      --copy---------->    dist/css/
 *   vendored deps    --copy---------->    dist/vendor/
 *   font subsets     --copy---------->    dist/fonts/
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
 * Files copied verbatim into dist/vendor/. npm-sourced entries are resolved through
 * node's resolver so a version bump in package.json cannot silently leave a stale
 * path behind.
 *
 * datastar is NOT on npm: the package named `datastar` is an unrelated GoDaddy
 * library, and `@starfederation/datastar` was abandoned at 1.0.0-beta.11 and never
 * became v1. The v1.x browser bundle ships only from the git repo, so it is
 * vendored into assets/vendor/ and committed — which also keeps the build hermetic
 * (D2). See docs/DECISIONS.md for the pinned version and its SRI hash.
 *
 * @picocss/pico used to be here. It is gone: its default type scale was the whole
 * of the "the CSS is huge" complaint, and catlog.css now carries the reset, the
 * tokens, the type scale, the theme and the form controls itself.
 */
const vendorFiles = [{ from: path.join(assetsDir, "vendor", "datastar.js"), to: "datastar.js" }];

/**
 * Font subsets copied into dist/fonts/.
 *
 * Inter Variable, self-hosted from fontsource, **latin subset only** — no CDN, so
 * the build stays hermetic (D2) exactly as the vendored datastar bundle does. One
 * 48 kB woff2 covers every glyph catlog can render: handles, kitten names and body
 * names are ASCII by construction (docs/events.md), and the only non-ASCII
 * characters in our own copy — em dash, ×, †, ↑, ↓ and · — all fall inside the
 * latin `unicode-range` (U+0000-00FF plus U+2000-206F plus U+2191/2193).
 * Verified against the package's own unicode.json rather than assumed.
 *
 * Group separators are no longer in that list because they are no longer ours:
 * intl.js re-renders every number in the reader's locale, so the separator can
 * be a U+202F (fr-FR), a U+00A0 (many), a full stop (de-DE) or an apostrophe
 * (de-CH). All four are inside the same two ranges, which is why this is a note
 * rather than a second subset.
 *
 * `@font-face` is declared in catlog.css against /static/fonts/, not copied from
 * the package's CSS, so the `src:` URL matches where this script actually put the
 * file.
 */
const fontFiles = [
  {
    from: require.resolve("@fontsource-variable/inter/files/inter-latin-wght-normal.woff2"),
    to: "inter-latin-wght-normal.woff2",
  },
];

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

  // --- css: copied as-is; there is no preprocessor and nothing to compile ---
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

  // --- fonts ---
  await mkdir(path.join(distDir, "fonts"), { recursive: true });
  for (const { from, to } of fontFiles) {
    await cp(from, path.join(distDir, "fonts", to));
  }
  console.log(`fonts:  ${fontFiles.length} file(s)`);

  console.log(`built   ${path.relative(process.cwd(), distDir)}`);
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
