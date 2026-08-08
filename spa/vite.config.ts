import { copyFile } from 'node:fs/promises';
import path from 'node:path';
import optimizeLocales from '@react-aria/optimize-locales-plugin';
import babel from '@rolldown/plugin-babel';
import tailwindcss from '@tailwindcss/vite';
import react, { reactCompilerPreset } from '@vitejs/plugin-react';
import type { Plugin } from 'vite';
import { defineConfig } from 'vitest/config';

/**
 * Where the catlog read API lives while `pnpm dev` is running. Only the dev
 * server proxies; a built bundle talks to VITE_CATLOG_API_BASE directly.
 */
const DEV_API_TARGET = process.env.CATLOG_DEV_API ?? 'http://127.0.0.1:8080';

/**
 * The path the bundle is served from.
 *
 * `/` by default: this SPA is deployed to its own host on its own domain, and
 * the router reads and writes real paths from the site root. A subpath
 * deployment — a GitHub Pages *project* page at
 * `https://<owner>.github.io/<repo>/`, say — sets `SPA_BASE=/<repo>/` and
 * everything follows: Vite rewrites the asset URLs, and `src/state/router.ts`
 * reads the same value back out of `import.meta.env.BASE_URL` to strip it from
 * locations and prepend it to links. Nothing in the source assumes `/`.
 */
const BASE = process.env.SPA_BASE ?? '/';

/**
 * Emits `dist/404.html` as a byte copy of `dist/index.html`.
 *
 * HTML5 routing needs the host to answer a deep link with the app rather than a
 * 404, and GitHub Pages has no rewrite rules — its one hook is a `404.html` at
 * the site root, which it serves for any unmatched path. The app then routes
 * from `location.pathname` and the user sees the page they asked for.
 *
 * Generated rather than committed: a checked-in duplicate of `index.html` drifts
 * the first time a script tag, a hash or `base` changes, and it drifts silently
 * — the deep link keeps "working" while serving a stale bundle. The build has
 * the real file, so the build makes the copy.
 *
 * `writeBundle` rather than `generateBundle`: by then Vite's HTML pipeline has
 * definitely finished with `index.html` and written it, so there is no plugin
 * ordering to get right. If it is missing, `copyFile` throws and the build fails
 * loudly — which is the correct outcome for a build that cannot ship its own
 * fallback.
 */
function deepLinkFallback(): Plugin {
  let outDir = 'dist';
  return {
    name: 'catlog:deep-link-fallback',
    apply: 'build',
    enforce: 'post',
    configResolved(config) {
      outDir = path.resolve(config.root, config.build.outDir);
    },
    async writeBundle() {
      await copyFile(path.join(outDir, 'index.html'), path.join(outDir, '404.html'));
    },
  };
}

export default defineConfig({
  base: BASE,
  build: {
    rollupOptions: {
      output: {
        // Pin React itself (plus its jsx/compiler runtimes and `scheduler`,
        // which live under the react/react-dom package directories) to one
        // stable `vendor` chunk. Without this, rolldown folds react-dom into
        // whatever shell chunk imports it, and every one-line edit to the shell
        // renames that chunk — invalidating ~140 KB of framework a returning
        // visitor's cache already held. React's release cadence is months; the
        // shell's is days. `advancedChunks` is rolldown's replacement for
        // Rollup's `manualChunks`, which rolldown-based Vite does not support.
        advancedChunks: {
          groups: [
            {
              name: 'vendor',
              test: /[/\\]node_modules[/\\](?:\.pnpm[/\\][^/\\]+[/\\]node_modules[/\\])?(?:react|react-dom|scheduler)[/\\]/,
            },
          ],
        },
      },
    },
  },
  // Explicit, because the deep links depend on it: Vite's dev and preview
  // servers answer an unmatched path with `index.html` in `spa` mode, which is
  // the local equivalent of the `404.html` above.
  appType: 'spa',
  plugins: [
    tailwindcss(),
    react(),
    // React Compiler. @vitejs/plugin-react@6 dropped the inline `babel` option,
    // so the compiler runs through @rolldown/plugin-babel using the preset that
    // ships with plugin-react (React 19 target, client-only, infer mode). The
    // Rules of React it depends on are enforced at lint time by
    // eslint-plugin-react-hooks — see .oxlintrc.json.
    babel({ presets: [reactCompilerPreset()] }),
    // react-aria-components ships its screen-reader strings — "3 results
    // available", "press Delete to remove" — pre-translated into every locale it
    // supports, and imports all of them eagerly. catlog is English only and
    // deliberately so: every timestamp on the site is fixed UTC because "a
    // leaderboard is a shared artefact, and localising it would make two people
    // describing the same row disagree". So the other locales are resolved to an
    // empty module rather than shipped to a browser that will never ask for them.
    //
    // `'en'` and not `'en-GB'`: the plugin matches a stripped locale by language
    // *and* region, so naming a region throws away `en-US` — which is what most
    // browsers actually report, and React Aria then finds no string table at all
    // and crashes on the first `Table`. A bare language keeps every English
    // variant and drops the other thirty-odd.
    //
    // `apply: 'build'` because it is a no-op in dev, where nothing is bundled.
    { ...optimizeLocales.vite({ locales: ['en'] }), apply: 'build' },
    deepLinkFallback(),
  ],
  server: {
    // The dev server is same-origin with the API, which is the point: `pnpm dev`
    // exercises the app without needing the server's CORS allow-list to be
    // right. `.env.development` sets VITE_CATLOG_API_BASE to the empty string so
    // requests come out as `/v1/…` and land here.
    proxy: {
      '/v1': { target: DEV_API_TARGET, changeOrigin: false },
    },
  },
  test: {
    environment: 'happy-dom',
    include: ['src/**/*.test.ts', 'src/**/*.test.tsx'],
    setupFiles: ['src/test/setup.ts'],
    globals: false,
  },
});
