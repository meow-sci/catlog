import babel from '@rolldown/plugin-babel';
import tailwindcss from '@tailwindcss/vite';
import react, { reactCompilerPreset } from '@vitejs/plugin-react';
import { defineConfig } from 'vitest/config';

/**
 * Where the catlog read API lives while `pnpm dev` is running. Only the dev
 * server proxies; a built bundle talks to VITE_CATLOG_API_BASE directly.
 */
const DEV_API_TARGET = process.env.CATLOG_DEV_API ?? 'http://127.0.0.1:8080';

/**
 * The path the bundle is served from.
 *
 * GitHub Pages project pages live at `https://<owner>.github.io/<repo>/`, so the
 * default is the repo name. The workflow passes the real one
 * (`SPA_BASE=/${{ github.event.repository.name }}/`) rather than trusting this,
 * and a fork under a different name needs no code change.
 */
const BASE = process.env.SPA_BASE ?? '/catlog/';

export default defineConfig({
  base: BASE,
  plugins: [
    tailwindcss(),
    react(),
    // React Compiler. @vitejs/plugin-react@6 dropped the inline `babel` option,
    // so the compiler runs through @rolldown/plugin-babel using the preset that
    // ships with plugin-react (React 19 target, client-only, infer mode). The
    // Rules of React it depends on are enforced at lint time by
    // eslint-plugin-react-hooks — see .oxlintrc.json.
    babel({ presets: [reactCompilerPreset()] }),
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
