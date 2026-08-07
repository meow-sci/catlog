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

  test("the boards index lists every board and links to it", async ({ page }) => {
    await page.goto("/boards");
    await expect(page.locator("#boards-title")).toBeVisible();

    const rows = page.locator("#boards-index tr.boards-row");
    // 12 launch boards plus the six per-cause RUD boards (§5.6).
    await expect(rows).toHaveCount(18);

    const lithobrake = page.locator(`#boards-index tr[data-stat="${LITHOBRAKE}"]`);
    await expect(lithobrake).toContainText("Biggest Lithobrake Survived");
    await lithobrake.locator("a.board-link").click();
    await expect(page).toHaveURL(new RegExp(`/boards/${LITHOBRAKE}$`));
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
    await expect(top.locator("td.value")).toHaveText(String(TOP_VALUE));
    // The fold's context blob is rendered, not dumped as JSON.
    await expect(top.locator("td.context")).toContainText("duna");

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
    await expect(row.locator("td.value")).toContainText(String(TOP_VALUE));

    // Every RUD cause is seeded, so the counter boards are populated too.
    await expect(page.locator(`#profile-stats tr[data-stat="rud_total"] td.value`)).toContainText("6");
  });

  test("a flagged flight scores nothing", async ({ page }) => {
    // The demo dataset deliberately contains a flagged flight with a 999 m/s
    // impact and a 99.9 g window. Neither may appear anywhere.
    await page.goto(`/boards/${LITHOBRAKE}`);
    await expect(page.locator("tr.board-row", { hasText: "999" })).toHaveCount(0);
    await page.goto("/boards/peak_g_survived");
    await expect(page.locator("tr.board-row", { hasText: "99.9" })).toHaveCount(0);

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
