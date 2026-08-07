# catlog — React reader (`spa/`)

A static, read-only, anonymous React SPA over the catlog public read API: the
leaderboards, one board at a time, a player's placements, and a live activity
feed. Four `GET /v1/…` endpoints, no login, no writes.

**This is a standalone application.** It has its own toolchain, its own
lockfile, its own build and its own deployment, and it is driven entirely by
pnpm from inside this directory. It does not read from `server/`, `site/` or
`mod/` at build time, it never invokes `make`, and it needs no Go or .NET
toolchain to install, lint, test or build. The root `Makefile` does not mention
it. The only thing it shares with the rest of the repo is an HTTP contract — the
read API and the CORS allow-list that lets a browser reach it — which is exactly
the seam two independently deployed things are supposed to have.

catlog has two frontends and they are independent by design: the server-rendered
datastar site (`site/` + `server/internal/web/`) is the other one. Same data, two
UI patterns, kept side by side so they can be compared. They can be hosted on
different hosts, on different domains, and neither requires the other to be
running.

## Commands

Requires Node 24+ and pnpm. **pnpm only** — never npm, npx or yarn.

```sh
pnpm install          # once
pnpm dev              # vite dev server, http://localhost:5173
pnpm build            # tsc -b && vite build → dist/
pnpm preview          # serve dist/ exactly as a static host would
pnpm typecheck        # tsc -b
pnpm lint             # oxlint  (incl. the React Compiler rules)
pnpm lint:fix         # oxlint --fix
pnpm fmt              # oxfmt
pnpm fmt:check        # oxfmt --check
pnpm test             # vitest, no browser, no network
pnpm check            # typecheck + lint + fmt:check + test, in that order
pnpm smoke            # real chromium against a built bundle and a live catlogd
```

`pnpm check` is what CI runs, spelled out one tool at a time so a failure names
the tool that failed.

## Pointing it at a server

`VITE_CATLOG_API_BASE` is the origin of the read API, baked in at build time.

| Value                    | Meaning                                                        |
| ------------------------ | -------------------------------------------------------------- |
| unset                    | `http://127.0.0.1:8080` — the local dev server (`.env`)        |
| `""`                     | same origin; requests come out as `/v1/…` (`.env.development`) |
| `https://catlog.example` | a deployed API                                                 |

`pnpm dev` uses the empty value on purpose: `vite.config.ts` proxies `/v1` to
`CATLOG_DEV_API` (default `http://127.0.0.1:8080`), so development runs
same-origin and does not depend on the server's CORS allow-list being right.

Anything else — a built bundle on another origin, `pnpm preview`, a real
deployment — is cross-origin, so **catlogd must list that origin** in
`[cors] allowed_origins` (`CATLOG_CORS_ALLOWED_ORIGINS`). The defaults already
cover Vite's loopback dev and preview ports.

A local server to talk to, from the repo root:

```sh
server/bin/catlogd -config server/catlogd.dev.toml &
curl -X POST http://127.0.0.1:6060/admin/seed      # the demo dataset
```

## Routing, and what a static host has to do about it

The router (`src/state/router.ts`) is HTML5 History over real paths — `/`,
`/boards`, `/boards/:stat`, `/p/:handle`. No fragments. In-app links are plain
`<a href>` elements intercepted by one delegated click listener, so middle-click,
cmd/ctrl-click, shift-click and "open in new tab" all keep working.

Real paths mean **the host must answer an unmatched path with `index.html`**
instead of a 404. Every static host supports this; each spells it differently:

| Host                        | What it needs                                                                                                             |
| --------------------------- | ------------------------------------------------------------------------------------------------------------------------- |
| GitHub Pages                | a `404.html` copy of `index.html` at the site root — **the build emits it**, see `deepLinkFallback()` in `vite.config.ts` |
| nginx                       | `location / { try_files $uri $uri/ /index.html; }`                                                                        |
| Netlify                     | `/*    /index.html   200` in `public/_redirects`                                                                          |
| Cloudflare Pages            | the same `_redirects` line, or the "Single Page App" preset                                                               |
| Vercel                      | `{ "rewrites": [{ "source": "/(.*)", "destination": "/index.html" }] }`                                                   |
| S3 + CloudFront             | error document `index.html`, or a 404→200 response mapping                                                                |
| `vite dev` / `vite preview` | nothing — `appType: 'spa'` already does it                                                                                |

Without that, deep links break and _nothing else does_: the app still works
perfectly as long as every visit starts at `/`. So test a deep link, not the home
page. `pnpm smoke` does exactly that, from a cold browser context.

### Base path

`base` defaults to `/` — its own domain, served from the root. A subpath
deployment sets `SPA_BASE`:

```sh
SPA_BASE=/catlog/ pnpm build
SPA_BASE=/catlog/ pnpm preview --port 4173 --strictPort
SPA_URL=http://localhost:4173/catlog/ pnpm smoke
```

`preview` re-reads `vite.config.ts`, so it needs `SPA_BASE` too — with it set for the
build only, preview serves at `/` while the HTML asks for `/catlog/assets/…`, and
every asset silently falls through to the SPA fallback.

The router reads the same value back from `import.meta.env.BASE_URL`, strips it
when reading a location and prepends it when writing a link. Nothing in the
source assumes `/`.

## Deploying

`pnpm build` produces a `dist/` of static files — `index.html`, `404.html`,
hashed assets — that can be served by anything. Set `VITE_CATLOG_API_BASE` at
build time, `SPA_BASE` if it is not at the root, and give the host the fallback
rule from the table above.

`.github/workflows/spa-pages.yml` does this for GitHub Pages: pnpm install,
typecheck, lint, format check, test, build, upload. It is inert until Pages is
enabled for the repository.

## House rules

The React Compiler is on and verified working (`src/test/reactCompiler.test.ts`
asserts on the compiled output, because every way the compiler can fail to run is
silent). That makes the Rules of React mandatory: no mutation during render, no
conditional hooks, and **no hand-written `useMemo`/`useCallback`/`memo`** — a
manual memo makes the compiler bail out of the whole component. `pnpm lint`
enforces this via `eslint-plugin-react-hooks`; see `.oxlintrc.json`.
