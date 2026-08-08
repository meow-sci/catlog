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
const demoSearch = await apiGet('/v1/players?q=demo&limit=50');
const aceEvents = await apiGet('/v1/players/demo_ace/events?limit=5');
const collection = await apiGet('/v1/stats');

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
    // A profile: every placement, and every rank *with its denominator*. `#3`
    // on its own says nothing — third of four is not third of four thousand —
    // so the `players` field the read API added is what is asserted here.
    path: '/p/demo_ace',
    expect: [
      ...acePlayer.stats.slice(0, 5).map((s) => s.title),
      `#${String(acePlayer.stats[0]?.rank ?? 1)}`,
      `of ${String(acePlayer.stats[0]?.players ?? 1)}`,
    ],
  },
  { path: '/p/demo_crasher', expect: [ANCHOR.title, ANCHOR.rendered] },
  // The whole index, board for board: the set is dynamic, so the assertion is
  // "the page shows what the server listed" rather than any particular names.
  { path: '/boards', expect: index.boards.map((b) => b.title) },
  { path: '/', expect: [ANCHOR.handle, 'Live activity'] },
  // §4.8 answers 404 identically for unknown, retired and banned handles.
  { path: '/p/definitely_not_a_player', expect: ['catlog has no public profile for'] },
  // A path no route matches renders the app's own not-found state — not the
  // host's 404 page, and not a blank screen.
  { path: '/not-a-real-page', expect: ['Nothing here', '/not-a-real-page'] },
  // Search, as a real linkable page. The expectation is the API's own answer,
  // so a seed change moves both sides together.
  { path: '/search?q=demo', expect: demoSearch.handles.slice(0, 5) },
  // A three-handle comparison, straight from a URL somebody could have pasted
  // into a chat window — which is the whole point of keeping the handles there.
  {
    path: '/compare?handles=demo_ace,demo_crasher,ghost_of_a_handle',
    expect: [
      'demo_ace',
      'demo_crasher',
      // `found: false` is a column, not an omission: a typo must not look like
      // a defeat.
      'ghost_of_a_handle',
      'no such player',
      'in the world',
    ],
  },
  // The collection census — the one page that is about catlog rather than about
  // a player. The type names come from the API, so a seed change moves both
  // sides together.
  {
    path: '/stats',
    expect: [
      'Stats of stats',
      'Events logged',
      // Everything on that page is a projection, and this row is where a reader
      // finds out how current it is.
      'Projector lag',
      ...collection.events.types.slice(0, 3).map((t) => t.type),
    ],
  },
  // The raw event log: the history every other endpoint has an opinion about.
  {
    path: '/p/demo_ace/events',
    expect: [
      'Everything',
      'demo_ace',
      'reported',
      ...aceEvents.events.slice(0, 3).map((e) => e.type),
    ],
  },
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

// ---------------------------------------------------------------------------
// The unit renderer, against the numbers the server actually sent.
//
// `data-value` on every value cell carries the exact float as the API sent it,
// and that is what this reads. Reconstructing a number by stripping non-digits
// out of the rendered text is the thing that breaks the moment a career-time
// board renders "5m 13s" — which is precisely what the renderer now does.
// ---------------------------------------------------------------------------
{
  await page.goto(urlFor(`/boards/${ANCHOR.stat}`), { waitUntil: 'domcontentloaded' });
  await textWith(page, ANCHOR.handle);
  const rendered = await page.$$eval('tr.board-row td.value', (cells) =>
    cells.map((c) => ({ value: c.getAttribute('data-value'), text: c.innerText.trim() })),
  );
  const wantValues = anchorBoard.rows.map((r) => String(r.value));
  const gotValues = rendered.map((r) => r.value);
  const ok = JSON.stringify(gotValues) === JSON.stringify(wantValues);
  console.log(
    `${ok ? 'ok  ' : 'FAIL'} every value cell carries the exact float the API sent ` +
      `(${gotValues.length} rows)`,
  );
  if (!ok)
    failures.push(
      `data-value mismatch: page ${JSON.stringify(gotValues)}, API ${JSON.stringify(wantValues)}`,
    );

  // Speed never scales and is grouped with U+202F; a career time becomes a
  // duration. Both are §4 rules the two frontends must agree on character for
  // character, and both are invisible in a screenshot until they are wrong.
  check('the anchor renders through the unit renderer', rendered[0]?.text ?? '', [ANCHOR.rendered]);

  // The career-time boards publish **milliseconds** — `"ms"` as a board unit is
  // the exact string that means metres-per-second as a payload *key* suffix.
  // Only `units.ForKey` knows the difference, and this is the board side of it.
  const careerBoard = index.boards.find((b) => (b.unit === 'ms' || b.unit === 's') && b.count > 0);
  if (careerBoard !== undefined) {
    const rows = await apiGet(`/v1/leaderboards/${careerBoard.stat}`);
    await page.goto(urlFor(`/boards/${careerBoard.stat}`), { waitUntil: 'domcontentloaded' });
    await textWith(page, rows.rows[0]?.handle ?? '');
    const cells = await page.$$eval('tr.board-row td.value', (cs) =>
      cs.map((c) => ({ value: Number(c.getAttribute('data-value')), text: c.innerText.trim() })),
    );
    for (const cell of cells.slice(0, 5)) {
      const seconds = careerBoard.unit === 'ms' ? cell.value / 1000 : cell.value;
      // Under a minute it is "37.5 s"; from a minute up it is two components.
      const wantsDuration = seconds >= 60;
      const looksLikeDuration = /\d+(y|d|h|m)\s\d+(d|h|m|s)/.test(cell.text);
      const good = wantsDuration ? looksLikeDuration : /\d\s(s|ms)$/.test(cell.text);
      console.log(
        `${good ? 'ok  ' : 'FAIL'} ${careerBoard.stat}: ${String(cell.value)} ${careerBoard.unit}` +
          ` renders ${JSON.stringify(cell.text)}`,
      );
      if (!good) {
        failures.push(
          `${careerBoard.stat}: ${String(cell.value)} rendered ${JSON.stringify(cell.text)}`,
        );
      }
    }
  }

  // The period selector: a window is a place, so each tab is a real link.
  const weekly = page.getByRole('tab', { name: 'weekly' });
  const href = await weekly.getAttribute('href');
  const periodOk = href !== null && href.includes('period=weekly');
  console.log(`${periodOk ? 'ok  ' : 'FAIL'} the window selector is a link (${String(href)})`);
  if (!periodOk) failures.push(`the weekly tab is not a link: ${String(href)}`);
  await weekly.click();
  await page.waitForTimeout(500);
  if (!page.url().includes('period=weekly')) {
    failures.push(`clicking the weekly tab did not reach it: ${page.url()}`);
  } else {
    console.log(`ok   clicking it navigates to ${new URL(page.url()).search}`);
  }
}

// ---------------------------------------------------------------------------
// Handle search, in the header, on every page.
//
// The one rule that is invisible until it is wrong: **no request below two
// characters**, because the server answers 400 rather than an empty 200. The
// network log is what proves it.
// ---------------------------------------------------------------------------
{
  const searches = [];
  const onSearch = (r) => {
    const url = new URL(r.url());
    if (url.pathname === '/v1/players' && url.searchParams.has('q'))
      searches.push(url.searchParams.get('q'));
  };
  page.on('request', onSearch);

  await page.goto(BASE, { waitUntil: 'domcontentloaded' });
  await textWith(page, ANCHOR.handle);

  const box = page.getByPlaceholder('Find a handle').first();
  await box.click();
  await box.type('d', { delay: 60 });
  await page.waitForTimeout(600);
  const belowFloor = searches.length === 0;
  console.log(
    `${belowFloor ? 'ok  ' : 'FAIL'} one character asked the server nothing ` +
      `(${JSON.stringify(searches)})`,
  );
  if (!belowFloor) {
    failures.push(
      `a one-character query reached the origin, which answers 400: ${JSON.stringify(searches)}`,
    );
  }

  await box.type('emo', { delay: 60 });
  const suggestion = page.getByRole('option', { name: ANCHOR.handle });
  try {
    await suggestion.waitFor({ timeout: 10_000 });
    console.log(`ok   typing "demo" suggested ${ANCHOR.handle}`);
  } catch {
    failures.push(`the search box never suggested ${ANCHOR.handle} for "demo"`);
  }
  await suggestion.click().catch(() => {});
  const landed = await textWith(page, 'Handle claimed');
  const wentToProfile = new URL(page.url()).pathname.endsWith(`/p/${ANCHOR.handle}`);
  console.log(`${wentToProfile ? 'ok  ' : 'FAIL'} picking a suggestion opened ${page.url()}`);
  if (!wentToProfile) failures.push(`picking a suggestion did not open the profile: ${page.url()}`);
  check('the profile the search reached', landed, [ANCHOR.handle, ANCHOR.title]);
  page.off('request', onSearch);
}

// ---------------------------------------------------------------------------
// The "me" handle, and the only property that matters about it: it survives a
// reload. It is one localStorage key, no account and no session — every public
// response is `s-maxage=30` to a shared cache, so there is no server-rendered
// personalisation available to either frontend even in principle.
// ---------------------------------------------------------------------------
{
  await page.goto(urlFor(`/p/${ANCHOR.handle}`), { waitUntil: 'domcontentloaded' });
  await textWith(page, 'Handle claimed');
  await page.getByRole('button', { name: 'This is me' }).click();
  await page.waitForTimeout(300);

  const stored = await page.evaluate(() => window.localStorage.getItem('catlog:me'));
  console.log(
    `${stored === ANCHOR.handle ? 'ok  ' : 'FAIL'} catlog:me = ${JSON.stringify(stored)}`,
  );
  if (stored !== ANCHOR.handle) failures.push(`catlog:me was ${JSON.stringify(stored)}`);

  await page.reload({ waitUntil: 'domcontentloaded' });
  const after = await textWith(page, 'This is you');
  // The header chip and the profile toggle both come back, from one storage key
  // and no session. `You:` and the handle are separate elements, so they are
  // separate needles.
  check('after a reload', after, ['You:', ANCHOR.handle, 'This is you']);

  // And the row on the board it applies to is marked as the viewer's own.
  await page.goto(urlFor(`/boards/${ANCHOR.stat}`), { waitUntil: 'domcontentloaded' });
  await textWith(page, ANCHOR.handle);
  const marked = await page
    .$eval(`tr.board-row[data-handle="${ANCHOR.handle}"]`, (row) =>
      row.className.includes('bg-wash-selected'),
    )
    .catch(() => false);
  console.log(`${marked ? 'ok  ' : 'FAIL'} the viewer's own row is highlighted on the board`);
  if (!marked) failures.push('the "me" row was not highlighted on the board page');

  // Forget it again so the rest of the script runs as an anonymous visitor.
  await page.evaluate(() => {
    window.localStorage.removeItem('catlog:me');
  });
}

// ---------------------------------------------------------------------------
// Privacy, on the one page that shows raw stored data.
//
// Redaction is the server's job and cannot be implemented in a frontend — this
// is a tripwire on the side that renders, not a substitute for it.
// ---------------------------------------------------------------------------
{
  await page.goto(urlFor(`/p/demo_ace/events`), { waitUntil: 'domcontentloaded' });
  await textWith(page, 'reported');
  // Open every payload disclosure, so the assertion covers what a reader can
  // actually reach rather than only what is visible on first paint.
  for (const trigger of await page.getByRole('button', { name: 'Payload' }).all()) {
    await trigger.click().catch(() => {});
  }
  const html = await page.content();
  for (const forbidden of ['user_key', '"install"', 'install_id', 'wall_t']) {
    const clean = !html.includes(forbidden);
    console.log(
      `${clean ? 'ok  ' : 'FAIL'} the raw event view never says ${JSON.stringify(forbidden)}`,
    );
    if (!clean) failures.push(`the raw event view rendered ${forbidden}`);
  }
  // What it *does* show: the events themselves, with raw payload numbers.
  check(
    'the raw event view',
    await page.locator('#root').innerText(),
    aceEvents.events.slice(0, 2).map((e) => e.type),
  );
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
