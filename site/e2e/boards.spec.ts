import { expect, test } from "@playwright/test";

/**
 * §8 spec 3 — the seeded demo data renders.
 *
 * This is the one spec `scripts/e2e-full.sh` also runs, against the instance the
 * simulator just flew into. That instance is seeded before this runs, so the
 * literal values below hold in both worlds; what differs is that `sim_ace` is
 * also on the lithobrake board there, at 62 m/s, below `demo_crasher`'s 214.
 * Hence "rank 1 is demo_crasher" rather than "there is exactly one row".
 */

/** From `internal/seed`: §5.6's own worked example. */
const LITHOBRAKE = "biggest_lithobrake_survived";
const TOP_HANDLE = "demo_crasher";
const TOP_VALUE = 214;

/**
 * The boards whose keys are compile-time constants — one per fold in
 * `server/internal/stats`. These, and only these, exist regardless of what
 * anybody has flown, so their disappearance is a regression and can be asserted
 * by name.
 *
 * The rest of the index cannot be: `fastest_to_<body>` and `rud_<cause>` take
 * their keys from the event stream, because KSA's celestial systems are content
 * that mods extend and the server treats a body name as opaque. A board appears
 * the day two players reach somewhere new. That is why the assertion below is a
 * required *set* plus an agreement check against the JSON, rather than the exact
 * row count it used to be — a count every new body invalidates is a count that
 * gets bumped without being read.
 */
const FIXED_BOARDS = [
  "biggest_lithobrake_survived",
  "peak_g_survived",
  "fastest_surface_speed",
  "fastest_orbital_speed",
  "kitten_tumbles",
  "rud_total",
  "orbits_achieved",
  "soi_bodies",
  "dockings",
  "stagings",
  "kittens_recovered",
  "distance_travelled",
  "fastest_to_orbit",
];

test.describe("boards", () => {
  test("the home page shows featured boards and the feed panel", async ({ page }) => {
    await page.goto("/");
    await expect(page.locator("#home-title")).toBeVisible();

    const featured = page.locator("#featured-boards .featured-board");
    await expect(featured).toHaveCount(3);
    await expect(page.locator(`#featured-boards .featured-board[data-stat="${LITHOBRAKE}"]`)).toBeVisible();
    await expect(page.locator("#feed-panel")).toBeVisible();
    await expect(page.locator("#feed")).toBeVisible();

    // The featured lithobrake board leads with the record holder.
    const top = page.locator(`.featured-board[data-stat="${LITHOBRAKE}"] tr.board-row`).first();
    await expect(top).toHaveAttribute("data-handle", TOP_HANDLE);
  });

  test("the boards index lists every board the server publishes, and links to it", async ({
    page,
  }) => {
    await page.goto("/boards");
    await expect(page.locator("#boards-title")).toBeVisible();

    // 1. Every board with a compile-time key is here. This is what catches a
    //    board that silently stopped being published, and it does not go stale
    //    when somebody flies somewhere new.
    for (const stat of FIXED_BOARDS) {
      await expect(page.locator(`#boards-index tr[data-stat="${stat}"]`)).toHaveCount(1);
    }

    // 2. The page and the JSON agree, board for board. Whatever the dynamic half
    //    of the index happens to be today, the HTML must be exactly it — no
    //    board dropped by the template, none invented by it.
    const api = await page.request.get("/v1/leaderboards");
    expect(api.ok()).toBeTruthy();
    const json = (await api.json()) as { boards: Array<{ stat: string }>; min_players: number };
    const published = json.boards.map((b) => b.stat);
    const rendered = await page
      .locator("#boards-index tr.boards-row")
      .evaluateAll((els) => els.map((el) => el.getAttribute("data-stat")));
    expect(rendered).toEqual(published);
    // And the demo dataset does exercise the dynamic half, or the check above
    // would be comparing two copies of the fixed list.
    expect(published.length).toBeGreaterThan(FIXED_BOARDS.length);
    expect(published.some((stat) => stat.startsWith("fastest_to_") && stat !== "fastest_to_orbit"))
      .toBeTruthy();
    expect(published.some((stat) => stat.startsWith("rud_") && stat !== "rud_total")).toBeTruthy();

    // 3. The threshold that keeps a one-entrant board out of the index is stated
    //    on the page, with the server's own number.
    expect(json.min_players).toBeGreaterThan(1);
    await expect(page.locator("#boards-note")).toContainText(String(json.min_players));

    const lithobrake = page.locator(`#boards-index tr[data-stat="${LITHOBRAKE}"]`);
    await expect(lithobrake).toContainText("Biggest Lithobrake Survived");
    await lithobrake.locator("a.board-link").click();
    await expect(page).toHaveURL(new RegExp(`/boards/${LITHOBRAKE}$`));
  });

  test("a board for a body nobody hard-coded exists, ranks, and says which way it reads", async ({
    page,
  }) => {
    // `luna` is not a constant anywhere in the server: the board exists because
    // two seeded players flew there. It is a career-time board, so the *smallest*
    // value ranks first — the opposite of every record board.
    await page.goto("/boards/fastest_to_luna");
    await expect(page.locator("#board-title")).toHaveAttribute("data-stat", "fastest_to_luna");
    await expect(page.locator("#board-title")).toHaveText("Fastest to Luna");
    await expect(page.locator("#board-direction")).toHaveAttribute("data-ascending", "true");
    await expect(page.locator("#board-direction")).toContainText("Lowest wins");

    // Read the figure out of `data-value`, never out of the rendered text.
    //
    // This board is a career-time board, so its unit is milliseconds and every
    // cell renders as a duration — "5m 13s", "1h 01m". Stripping non-digits out
    // of that produces 513 and 101, which happen to sort, so the old assertion
    // would have gone on passing while asserting nothing. Every value cell now
    // carries the exact float the API published; that is what to compare.
    const values = await page
      .locator("tr.board-row td.value")
      .evaluateAll((els) => els.map((el) => Number((el as HTMLElement).dataset.value)));
    expect(values.length).toBeGreaterThan(1);
    expect(values.every((v) => Number.isFinite(v))).toBeTruthy();
    expect(values).toEqual([...values].sort((a, b) => a - b));

    // And the cells really are rendered as durations, which is the whole reason
    // the assertion above had to move.
    const rendered = await page.locator("tr.board-row td.value").first().innerText();
    expect(rendered).toMatch(/^\d[\d .]*\s?(ms|s)$|^\d+[dhmy]\s\d+[dhms]$/);

    // The HTML agrees with the JSON, direction included.
    const json = (await (await page.request.get("/v1/leaderboards/fastest_to_luna")).json()) as {
      ascending: boolean;
      rows: Array<{ handle: string }>;
    };
    expect(json.ascending).toBe(true);
    const handles = await page
      .locator("tr.board-row")
      .evaluateAll((els) => els.map((el) => el.getAttribute("data-handle")));
    expect(handles).toEqual(json.rows.map((r) => r.handle));
  });

  test("the lithobrake board is ranked, and the top row is the seeded record", async ({ page }) => {
    await page.goto(`/boards/${LITHOBRAKE}`);
    await expect(page.locator("#board-title")).toHaveAttribute("data-stat", LITHOBRAKE);

    const rows = page.locator("tr.board-row");
    const count = await rows.count();
    expect(count).toBeGreaterThan(0);

    // Ranks are positional over visible rows and must run 1..n with no holes:
    // a banned player closes the gap rather than leaving one (§4.8).
    const ranks = await rows.evaluateAll((els) => els.map((el) => Number(el.getAttribute("data-rank"))));
    expect(ranks).toEqual(Array.from({ length: count }, (_, i) => i + 1));

    const top = rows.first();
    await expect(top).toHaveAttribute("data-handle", TOP_HANDLE);
    // The exact figure lives in `data-value`; the cell reads "214 m/s", because
    // a value cell carries its own unit wherever it appears.
    await expect(top.locator("td.value")).toHaveAttribute("data-value", String(TOP_VALUE));
    await expect(top.locator("td.value")).toContainText(`${TOP_VALUE} m/s`);
    // The fold's context blob is rendered as pairs, not dumped as JSON, and body
    // names are title-cased to match the board titles the server generates.
    await expect(top.locator("td.context")).toContainText("Duna");
    // `flight` is a client-minted ULID: out of the default table, still inside
    // the row's Details disclosure.
    await expect(top.locator("td.context .ctx-key", { hasText: "flight" })).toHaveCount(0);
    await expect(top.locator("td.context details.details-blob")).toHaveCount(1);

    // And the HTML agrees with the JSON the same server publishes.
    const api = await page.request.get(`/v1/leaderboards/${LITHOBRAKE}`);
    expect(api.ok()).toBeTruthy();
    const json = await api.json();
    expect(json.rows[0].handle).toBe(TOP_HANDLE);
    expect(json.rows[0].value).toBe(TOP_VALUE);
    expect(api.headers()["cache-control"]).toContain("s-maxage=30");
  });

  test("a profile shows the player's records and ranks", async ({ page }) => {
    await page.goto(`/p/${TOP_HANDLE}`);
    await expect(page.locator("#profile-handle")).toHaveText(TOP_HANDLE);

    const row = page.locator(`#profile-stats tr[data-stat="${LITHOBRAKE}"]`);
    await expect(row).toBeVisible();
    await expect(row).toHaveAttribute("data-rank", "1");
    await expect(row.locator("td.value")).toHaveAttribute("data-value", String(TOP_VALUE));

    // A rank now carries its denominator, which is what turns "#1" into a fact.
    await expect(row.locator("td.rank")).toContainText(/#1 of \d+/);
    await expect(row.locator("td.rank")).not.toHaveAttribute("data-players", "0");

    // Every RUD cause is seeded, so the counter boards are populated too.
    const ruds = page.locator(`#profile-stats tr[data-stat="rud_total"] td.value`);
    await expect(ruds).toHaveAttribute("data-value", "6");
    await expect(ruds).toContainText("6 RUDs");
  });

  test("a flagged flight scores nothing", async ({ page }) => {
    // The demo dataset deliberately contains a flagged flight with a 999 m/s
    // impact and a 99.9 g window. Neither may appear anywhere.
    //
    // Asserted on `data-value` rather than on the rendered text: "999" as text
    // would also match a grouped "1 999" and would not match a scaled cell at
    // all, so the attribute is both stricter and more honest.
    await page.goto(`/boards/${LITHOBRAKE}`);
    await expect(page.locator('tr.board-row td.value[data-value="999"]')).toHaveCount(0);
    await page.goto("/boards/peak_g_survived");
    await expect(page.locator('tr.board-row td.value[data-value="99.9"]')).toHaveCount(0);

    // Same fact through the JSON, where the value is a number rather than text.
    for (const stat of [LITHOBRAKE, "peak_g_survived"]) {
      const json = await (await page.request.get(`/v1/leaderboards/${stat}`)).json();
      for (const row of json.rows as Array<{ value: number }>) {
        expect(row.value).toBeLessThan(900);
      }
    }
  });

  test("an unknown handle renders the 404 page", async ({ page }) => {
    const res = await page.goto("/p/no_such_player_at_all");
    expect(res?.status()).toBe(404);
    await expect(page.locator("#not-found")).toBeVisible();
    await expect(page.locator("#not-found-detail")).toContainText(/No such player/i);
    await expect(page.locator("#not-found-home")).toHaveAttribute("href", "/");
  });

  test("an unknown board and an unknown page render the 404 page", async ({ page }) => {
    let res = await page.goto("/boards/not_a_board");
    expect(res?.status()).toBe(404);
    await expect(page.locator("#not-found")).toBeVisible();

    res = await page.goto("/definitely/not/a/page");
    expect(res?.status()).toBe(404);
    await expect(page.locator("#not-found")).toBeVisible();
  });

  test("the docs pages render, and privacy states the email guarantee", async ({ page }) => {
    await page.goto("/docs/install");
    await expect(page.locator("#docs-title")).toContainText("Installing catlog");

    await page.goto("/docs/privacy");
    await expect(page.locator("#privacy-no-email")).toContainText(
      /catlog never receives your email address/i,
    );
    await expect(page.locator("#privacy-scopes")).toContainText("identify");

    await page.goto("/docs/api");
    await expect(page.locator("#docs-api-endpoints")).toContainText("/v1/feed/sse");
  });
});
