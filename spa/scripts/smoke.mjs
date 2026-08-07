// Drives the *built* SPA in a real browser against a real, seeded catlogd.
//
// This is not part of `pnpm test`: it needs a running server, a seeded database
// and a downloaded chromium, none of which a unit test may assume. It is the
// same split the repo already makes between `make test` and `make e2e`.
//
// Run it like this, from the repo root:
//
//   server/bin/catlogd -config server/catlogd.dev.toml &     # or: make dev
//   curl -X POST http://127.0.0.1:6060/admin/seed
//   VITE_CATLOG_API_BASE=http://127.0.0.1:8080 pnpm -C spa build
//   pnpm -C spa preview --port 4173 --strictPort &
//   pnpm -C spa smoke
//
// `make e2e-browser` (or `pnpm -C site exec playwright install chromium`)
// provides the browser.
//
// It asserts against the deterministic demo dataset `POST /admin/seed` inserts,
// so a change to that fixture is supposed to break this.
import { chromium } from 'playwright';

const BASE = process.env.SPA_URL ?? 'http://localhost:4173/catlog/';
const ADMIN = process.env.ADMIN_URL ?? 'http://127.0.0.1:6060';

/** The seeded records, verbatim from server/internal/seed. */
const SEEDED = [
  { hash: '#/boards/biggest_lithobrake_survived', expect: ['demo_crasher', '214 m/s'] },
  { hash: '#/boards/orbits_achieved', expect: ['demo_ace', '2 orbits'] },
  { hash: '#/boards/kitten_tumbles', expect: ['demo_tumbler', '4 tumbles'] },
  { hash: '#/p/demo_ace', expect: ['Orbits Achieved', 'rank #1', '2 orbits'] },
  { hash: '#/p/demo_crasher', expect: ['Biggest Lithobrake Survived', '214 m/s'] },
  { hash: '#/boards', expect: ['Biggest Lithobrake Survived', 'Kitten Tumbles', 'Dockings'] },
  { hash: '#/', expect: ['demo_crasher', 'Live activity'] },
  // §4.8 answers 404 identically for unknown, retired and banned handles.
  { hash: '#/p/definitely_not_a_player', expect: ['No such player'] },
];

/**
 * Matches against `innerText`, which is the *rendered* text — CSS
 * `text-transform: uppercase` has already been applied to it, so the comparison
 * has to ignore case or every panel heading fails.
 */
const contains = (haystack, needle) => haystack.toLowerCase().includes(needle.toLowerCase());

const failures = [];
const browser = await chromium.launch();
const page = await browser.newPage();

// Anything the browser complains about is a failure: a CORS refusal, a bad
// asset path from a wrong `base`, or a React error all surface here and nowhere
// else.
page.on('pageerror', (e) => failures.push(`pageerror: ${e.message}`));
// A 4xx for a *static asset* means `base` is wrong for wherever this is hosted,
// which is the single most likely way a GitHub Pages deployment breaks. API
// responses are excluded: one of the cases below deliberately asks for a player
// that does not exist, and under `pnpm dev` the API is proxied through this very
// origin, so the two cannot be told apart by host alone.
const SPA_ORIGIN = new URL(BASE).origin;
const isAsset = (url) => url.startsWith(SPA_ORIGIN) && !new URL(url).pathname.startsWith('/v1/');
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

for (const { hash, expect } of SEEDED) {
  await page.goto(BASE + hash, { waitUntil: 'domcontentloaded' });
  let text = '';
  try {
    await page.waitForFunction(
      (needle) =>
        (document.querySelector('#root')?.textContent ?? '')
          .toLowerCase()
          .includes(needle.toLowerCase()),
      expect[0],
      { timeout: 15_000 },
    );
    text = await page.locator('#root').innerText();
  } catch {
    text = await page.locator('#root').innerText();
  }
  for (const needle of expect) {
    const ok = contains(text, needle);
    console.log(`${ok ? 'ok  ' : 'FAIL'} ${hash} → ${JSON.stringify(needle)}`);
    if (!ok) failures.push(`${hash}: expected ${JSON.stringify(needle)} in\n${text}`);
  }
}

// The live feed, end to end: push one event through the loopback admin API and
// watch it arrive over `GET /v1/feed/stream`. This is the half of the app that
// no unit test can cover, because the whole question is whether the SSE
// transport is real.
await page.goto(BASE + '#/', { waitUntil: 'domcontentloaded' });
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
  await page.goto(BASE + '#/', { waitUntil: 'domcontentloaded' });
  await page.waitForFunction(
    () => (document.querySelector('#root')?.textContent ?? '').includes('demo_crasher'),
    undefined,
    { timeout: 15_000 },
  );
  await page.screenshot({ path: process.env.SMOKE_SHOT, fullPage: true });
  console.log(`ok   screenshot written to ${process.env.SMOKE_SHOT}`);
}

await browser.close();

if (failures.length > 0) {
  console.error(`\n${String(failures.length)} failure(s):\n` + failures.join('\n\n'));
  process.exit(1);
}
console.log('\nsmoke: all checks passed');
