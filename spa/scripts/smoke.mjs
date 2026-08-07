// Drives the *built* SPA in a real browser against a real, seeded catlogd.
//
// This is not part of `pnpm test`: it needs a running server, a seeded database
// and a downloaded chromium, none of which a unit test may assume.
//
// The server is a *runtime* dependency of this script and of nothing else: the
// SPA installs, lints, tests and builds with no Go toolchain anywhere near it.
// Start one however you like — this is the catlog dev server, from the repo root:
//
//   server/bin/catlogd -config server/catlogd.dev.toml &
//   curl -X POST http://127.0.0.1:6060/admin/seed
//
// then, from this directory:
//
//   VITE_CATLOG_API_BASE=http://127.0.0.1:8080 pnpm build
//   pnpm preview --port 4173 --strictPort &
//   pnpm smoke
//
// `pnpm exec playwright install chromium` provides the browser. It runs equally
// well against `pnpm dev` — point SPA_URL at http://localhost:5173/.
//
// SPA_URL points at wherever the bundle is being served, *including* the base
// path it was built with — `http://localhost:4173/` by default, or
// `http://localhost:4173/catlog/` for a subpath build. Every URL below is
// derived from it, so the same script proves either deployment shape.
//
// It asserts against the deterministic demo dataset `POST /admin/seed` inserts.
//
// Most of what it expects is *fetched from the read API at run time* and then
// looked for on screen, because the question a browser smoke test answers is
// "does the SPA render what the server said", not "does the server still say the
// number somebody typed here in 2026". That matters more than it used to: the
// board index is now assembled from the data — catlog keeps no list of celestial
// bodies — so its membership changes as people fly, and any list of board names
// hard-coded here would be wrong within a week.
//
// One value stays hard-coded on purpose. `ANCHOR` below is §5.6's own worked
// example, and it is the tripwire that says the demo dataset is still the
// dataset: if `server/internal/seed/seed.go` stops producing it, this fails
// loudly and says so. Change it there and here together.
import { chromium } from 'playwright';

const BASE = (process.env.SPA_URL ?? 'http://localhost:4173/').replace(/\/*$/, '/');
const ADMIN = process.env.ADMIN_URL ?? 'http://127.0.0.1:6060';
// The read API the bundle was built against (VITE_CATLOG_API_BASE). Under
// `pnpm dev` the API is proxied through the app's own origin, so point this at
// SPA_URL in that case.
const API = (process.env.CATLOG_API_URL ?? 'http://127.0.0.1:8080').replace(/\/+$/, '');

/** An absolute URL for an app-relative path like `/boards/rud_total`. */
const urlFor = (path) => new URL(path.replace(/^\//, ''), BASE).toString();

/**
 * The one hard-coded seeded value: §5.6's worked example, from
 * `server/internal/seed/seed.go`. Everything else is read back from the API.
 */
const ANCHOR = {
  stat: 'biggest_lithobrake_survived',
  title: 'Biggest Lithobrake Survived',
  handle: 'demo_crasher',
  value: 214,
  rendered: '214 m/s',
};

async function apiGet(path) {
  const res = await fetch(API + path);
  if (!res.ok) throw new Error(`GET ${API + path} → ${String(res.status)}`);
  return res.json();
}

/**
 * Matches against `innerText`, which is the *rendered* text — CSS
 * `text-transform: uppercase` has already been applied to it, so the comparison
 * has to ignore case or every panel heading fails.
 */
const contains = (haystack, needle) => haystack.toLowerCase().includes(needle.toLowerCase());

const failures = [];
const browser = await chromium.launch();

/** Wires the console/network watchers every page in this script needs. */
function watch(page) {
  // Anything the browser complains about is a failure: a CORS refusal, a bad
  // asset path from a wrong `base`, or a React error all surface here and
  // nowhere else.
  page.on('pageerror', (e) => failures.push(`pageerror: ${e.message}`));
  // A 4xx for a *static asset* means `base` is wrong for wherever this is
  // hosted, or that a relative URL resolved against a nested route — the two
  // most likely ways HTML5 routing breaks a static deployment. API responses are
  // excluded: one of the cases above deliberately asks for a player that does
  // not exist, and under `pnpm dev` the API is proxied through this very origin,
  // so the two cannot be told apart by host alone.
  const isAsset = (url) =>
    url.startsWith(new URL(BASE).origin) && !new URL(url).pathname.startsWith('/v1/');
  page.on('response', (r) => {
    if (r.status() >= 400 && isAsset(r.url())) {
      failures.push(`${String(r.status())} ${r.url()} — check vite's \`base\``);
    }
    // A 5xx from anywhere, including the API, is always a failure.
    if (r.status() >= 500) failures.push(`${String(r.status())} ${r.url()}`);
  });
  page.on('requestfailed', (r) => {
    const why = r.failure()?.errorText ?? '?';
    // An abort is the app's own cleanup, not a failure. React's StrictMode
    // double-invokes effects in a development build, so the first fetch of every
    // page is cancelled by design — running this against `pnpm dev` would
    // otherwise report a handful of phantom errors. (A production build does not
    // do this; StrictMode is inert there.)
    if (why === 'net::ERR_ABORTED') return;
    failures.push(`request failed: ${r.url()} — ${why}`);
  });
  return page;
}

/** Waits until the rendered app contains `needle`; returns whatever it rendered. */
async function textWith(page, needle) {
  try {
    await page.waitForFunction(
      (want) =>
        (document.querySelector('#root')?.textContent ?? '')
          .toLowerCase()
          .includes(want.toLowerCase()),
      needle,
      { timeout: 15_000 },
    );
  } catch {
    // Fall through: the assertion below reports what was actually on screen.
  }
  return page.locator('#root').innerText();
}

/** Asserts `needles` are all present, and logs one line per needle. */
function check(label, text, needles) {
  for (const needle of needles) {
    const ok = contains(text, needle);
    console.log(`${ok ? 'ok  ' : 'FAIL'} ${label} → ${JSON.stringify(needle)}`);
    if (!ok) failures.push(`${label}: expected ${JSON.stringify(needle)} in\n${text}`);
  }
}

const page = watch(await browser.newPage());

// ---------------------------------------------------------------------------
// What the server says, first. The page assertions below are built from it.
// ---------------------------------------------------------------------------
const index = await apiGet('/v1/leaderboards');
const anchorBoard = await apiGet(`/v1/leaderboards/${ANCHOR.stat}`);
const acePlayer = await apiGet('/v1/players/demo_ace');

{
  const top = anchorBoard.rows[0];
  const ok = top?.handle === ANCHOR.handle && top?.value === ANCHOR.value;
  console.log(
    `${ok ? 'ok  ' : 'FAIL'} the demo dataset is the one this script knows: ` +
      `${ANCHOR.stat} rank 1 = ${String(top?.handle)} at ${String(top?.value)}`,
  );
  if (!ok) {
    failures.push(
      `the seeded anchor moved: expected ${ANCHOR.handle} at ${String(ANCHOR.value)} on ` +
        `${ANCHOR.stat}, got ${JSON.stringify(top)}. If server/internal/seed/seed.go changed ` +
        `on purpose, update ANCHOR at the top of this file.`,
    );
  }
  if (index.boards.length === 0) failures.push('GET /v1/leaderboards listed no boards at all');
}

/** A board's top row as the app renders the handle: the API is the expectation. */
const topHandle = (board) => board.rows[0]?.handle;

const SEEDED = [
  { path: `/boards/${ANCHOR.stat}`, expect: [ANCHOR.handle, ANCHOR.rendered] },
  // Everything below is whatever the server currently says, so a seed change is
  // not a smoke failure — only a *rendering* difference is.
  ...index.boards
    .filter((b) => b.count > 0)
    .slice(0, 4)
    .map((b) => ({ path: `/boards/${b.stat}`, expect: [b.title] })),
  {
    path: '/p/demo_ace',
    expect: [
      ...acePlayer.stats.slice(0, 5).map((s) => s.title),
      `rank #${String(acePlayer.stats[0]?.rank ?? 1)}`,
    ],
  },
  { path: '/p/demo_crasher', expect: [ANCHOR.title, ANCHOR.rendered] },
  // The whole index, board for board: the set is dynamic, so the assertion is
  // "the page shows what the server listed" rather than any particular names.
  { path: '/boards', expect: index.boards.map((b) => b.title) },
  { path: '/', expect: [ANCHOR.handle, 'Live activity'] },
  // §4.8 answers 404 identically for unknown, retired and banned handles.
  { path: '/p/definitely_not_a_player', expect: ['No such player'] },
  // A path no route matches renders the app's own not-found state — not the
  // host's 404 page, and not a blank screen.
  { path: '/not-a-real-page', expect: ['Page not found', '/not-a-real-page'] },
];

for (const { path, expect } of SEEDED) {
  await page.goto(urlFor(path), { waitUntil: 'domcontentloaded' });
  check(path, await textWith(page, expect[0]), expect);
}

// A board page whose rows the server sent must show the server's top handle.
for (const board of index.boards.filter((b) => b.count > 0).slice(0, 2)) {
  const rows = await apiGet(`/v1/leaderboards/${board.stat}`);
  const want = topHandle(rows);
  if (want === undefined) continue;
  await page.goto(urlFor(`/boards/${board.stat}`), { waitUntil: 'domcontentloaded' });
  check(`/boards/${board.stat} (top row from the API)`, await textWith(page, want), [want]);
}

// ---------------------------------------------------------------------------
// Deep link, cold.
//
// The point of the whole exercise. A brand-new browser context — no history, no
// storage, nothing cached, and above all no visit to the home page first — is
// sent straight to a nested URL, the way a pasted link arrives. The host has to
// answer it with the app (404.html on Pages, `appType: 'spa'` under preview) and
// the router has to read `location.pathname` and render the right board. With
// hash routing this case could not fail, because the path was always `/`; with
// real paths it is the case that can, so it gets its own context.
// ---------------------------------------------------------------------------
{
  const context = await browser.newContext();
  const cold = watch(await context.newPage());
  const target = urlFor(`/boards/${ANCHOR.stat}`);
  const response = await cold.goto(target, { waitUntil: 'domcontentloaded' });
  const text = await textWith(cold, ANCHOR.handle);

  console.log(`ok   cold deep link: GET ${target} → ${String(response?.status() ?? '?')}`);
  check('deep link (cold context, no home page first)', text, [ANCHOR.handle, ANCHOR.rendered]);
  if (new URL(cold.url()).pathname !== new URL(target).pathname) {
    failures.push(`the deep link was redirected: asked for ${target}, ended at ${cold.url()}`);
  }
  await context.close();
}

// ---------------------------------------------------------------------------
// Back and forward, across several navigations, through real links.
//
// Also the proof that link interception works at all: every step here is a
// plain left-click on an `<a href>` that the router turned into a pushState.
// ---------------------------------------------------------------------------
{
  await page.goto(BASE, { waitUntil: 'domcontentloaded' });
  await textWith(page, ANCHOR.handle);

  const pathOf = () => new URL(page.url()).pathname;
  const appPath = (path) => new URL(urlFor(path)).pathname;

  await page.getByRole('link', { name: 'Boards', exact: true }).click();
  await textWith(page, 'Every board catlog publishes');
  const atBoards = pathOf();

  await page
    .getByRole('link', { name: new RegExp(ANCHOR.title) })
    .first()
    .click();
  await textWith(page, ANCHOR.handle);
  const atBoard = pathOf();

  // A link inside the React Aria table — the one place a component library
  // could plausibly eat the click before the router sees it.
  await page.getByRole('link', { name: ANCHOR.handle }).first().click();
  const atPlayer = await textWith(page, 'Handle claimed');
  const playerPath = pathOf();

  const steps = [
    ['clicked  Boards', atBoards, appPath('/boards')],
    ['clicked  a board', atBoard, appPath(`/boards/${ANCHOR.stat}`)],
    ['clicked  a handle', playerPath, appPath(`/p/${ANCHOR.handle}`)],
  ];
  for (const [label, got, want] of steps) {
    const ok = got === want;
    console.log(`${ok ? 'ok  ' : 'FAIL'} ${label} → ${got}`);
    if (!ok) failures.push(`${label}: expected ${want}, got ${got}`);
  }
  check('clicked through to the profile', atPlayer, [ANCHOR.handle, ANCHOR.title]);

  // Three pushStates deep; walk all the way back out and all the way forward in.
  const walk = [
    ['back    ', () => page.goBack(), appPath(`/boards/${ANCHOR.stat}`), 'Standings'],
    ['back    ', () => page.goBack(), appPath('/boards'), 'Every board catlog publishes'],
    ['back    ', () => page.goBack(), appPath('/'), 'Leaderboards for things that went wrong'],
    ['forward ', () => page.goForward(), appPath('/boards'), 'Every board catlog publishes'],
    ['forward ', () => page.goForward(), appPath(`/boards/${ANCHOR.stat}`), ANCHOR.rendered],
    ['forward ', () => page.goForward(), appPath(`/p/${ANCHOR.handle}`), 'Handle claimed'],
  ];
  for (const [label, move, wantPath, wantText] of walk) {
    await move();
    const text = await textWith(page, wantText);
    const got = pathOf();
    const ok = got === wantPath && contains(text, wantText);
    console.log(`${ok ? 'ok  ' : 'FAIL'} ${label} → ${got} (${JSON.stringify(wantText)})`);
    if (!ok) {
      failures.push(
        `${label.trim()}: expected ${wantPath} showing ${JSON.stringify(wantText)}, ` +
          `got ${got} showing:\n${text}`,
      );
    }
  }
}

// ---------------------------------------------------------------------------
// cmd/ctrl-click must fall through to the browser.
//
// The router intercepts a *plain* left-click and nothing else. If it swallowed
// this one, the current page would navigate — so the assertion is that it did
// not: same URL, same rendered view, afterwards.
// ---------------------------------------------------------------------------
{
  await page.goto(urlFor('/boards'), { waitUntil: 'domcontentloaded' });
  await textWith(page, 'Every board catlog publishes');

  const before = page.url();
  const opened = [];
  const onPopup = (p) => opened.push(p);
  page.context().on('page', onPopup);

  await page
    .getByRole('link', { name: new RegExp(ANCHOR.title) })
    .first()
    .click({ modifiers: ['ControlOrMeta'] });
  // No navigation to wait for — that is the point — so give the router the
  // chance to get it wrong.
  await page.waitForTimeout(750);

  const after = page.url();
  const still = await page.locator('#root').innerText();
  const ok = after === before && contains(still, 'Every board catlog publishes');
  console.log(
    `${ok ? 'ok  ' : 'FAIL'} cmd/ctrl-click fell through to the browser ` +
      `(this page stayed at ${after}, ${String(opened.length)} new tab(s) opened)`,
  );
  if (!ok) {
    failures.push(
      `cmd/ctrl-click was swallowed by the router: ${before} → ${after}\n${still.slice(0, 400)}`,
    );
  }
  page.context().off('page', onPopup);
  for (const p of opened) await p.close();
}

// The live feed, end to end: push one event through the loopback admin API and
// watch it arrive over `GET /v1/feed/stream`. This is the half of the app that
// no unit test can cover, because the whole question is whether the SSE
// transport is real.
await page.goto(BASE, { waitUntil: 'domcontentloaded' });
await page.waitForFunction(
  () => (document.querySelector('#root')?.textContent ?? '').includes('live'),
  undefined,
  { timeout: 15_000 },
);
console.log('ok   feed reached status "live" (EventSource connected)');

// A `vehicle.rud` for demo_crasher on a body no seeded row mentions: it moves
// only the RUD boards, which nothing above asserts on, so this script stays
// runnable more than once against the same database.
const res = await fetch(`${ADMIN}/admin/events`, {
  method: 'POST',
  headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify({
    handle: 'demo_crasher',
    events: [
      {
        type: 'vehicle.rud',
        ver: 1,
        payload: { cause: 'collision', body: 'ike', speed_ms: 1234, crew_count: 0 },
      },
    ],
  }),
});
if (!res.ok) {
  failures.push(`POST ${ADMIN}/admin/events → ${res.status} ${await res.text()}`);
} else {
  try {
    await page.waitForFunction(
      () => {
        const rows = document.querySelectorAll('[aria-live="polite"] li');
        return rows.length > 0 && (rows[0]?.textContent ?? '').includes('ike');
      },
      undefined,
      { timeout: 20_000 },
    );
    console.log('ok   a new event arrived over SSE and was prepended to the feed');
  } catch {
    const feed = await page
      .locator('[aria-live="polite"]')
      .innerText()
      .catch(() => '(no feed list)');
    failures.push(`the pushed event never reached the feed. Panel was:\n${feed}`);
  }
}

// A picture, for a human who wants to see it rather than read about it.
if (process.env.SMOKE_SHOT !== undefined && process.env.SMOKE_SHOT !== '') {
  await page.setViewportSize({ width: 1280, height: 1000 });
  // Never `networkidle`: the SSE feed holds a connection open for the life of
  // the page, so the network is never idle by design.
  await page.goto(BASE, { waitUntil: 'domcontentloaded' });
  await textWith(page, ANCHOR.handle);
  await page.screenshot({ path: process.env.SMOKE_SHOT, fullPage: true });
  console.log(`ok   screenshot written to ${process.env.SMOKE_SHOT}`);
}

await browser.close();

if (failures.length > 0) {
  console.error(`\n${String(failures.length)} failure(s):\n` + failures.join('\n\n'));
  process.exit(1);
}
console.log('\nsmoke: all checks passed');
