import { expect, test } from "@playwright/test";

/**
 * The three journeys the redesign is for: see my own stats, see everyone's, and
 * see how I compare with my friends.
 *
 * Everything here is against the seeded demo dataset, whose handles are
 * `demo_crasher`, `demo_ace` and `demo_tumbler` (`internal/seed`).
 */

const ME = "demo_crasher";
const FRIEND = "demo_ace";
const THIRD = "demo_tumbler";
const LITHOBRAKE = "biggest_lithobrake_survived";

test.describe("units and numerals", () => {
  test("a career-time board renders a duration and still publishes the exact figure", async ({
    page,
  }) => {
    // `fastest_to_luna` is in milliseconds. Nothing on the page says "37500".
    await page.goto("/boards/fastest_to_luna");
    const cell = page.locator("tr.board-row td.value").first();

    const exact = await cell.getAttribute("data-value");
    const shown = (await cell.innerText()).trim();
    expect(Number(exact)).toBeGreaterThan(0);
    expect(shown).not.toBe(exact);

    // The unit is inside the rendered string, and `title` carries the raw figure
    // in the unit the event was in — which is how a reader recovers the digits.
    await expect(cell).toHaveAttribute("title", new RegExp(`^${exact} ms$`));

    // Tabular figures are what make a leaderboard column read as a column, and
    // the property has to be `font-variant-numeric` — `font-feature-settings`
    // does not compose and fails invisibly.
    const numeric = await cell.evaluate((el) => getComputedStyle(el).fontVariantNumeric);
    expect(numeric).toContain("tabular-nums");

    // Prose gets none of it.
    await page.goto("/");
    const summary = page.locator("#feed .feed-summary").first();
    if ((await summary.count()) > 0) {
      const prose = await summary.evaluate((el) => getComputedStyle(el).fontVariantNumeric);
      expect(prose).not.toContain("tabular-nums");
    }
  });

  test("Inter is served from this origin and actually loads", async ({ page }) => {
    // D2: the build is hermetic. Nothing on any page may come from a CDN.
    const external: string[] = [];
    page.on("request", (req) => {
      const url = new URL(req.url());
      if (url.hostname !== "127.0.0.1" && url.hostname !== "localhost") external.push(req.url());
    });

    await page.goto("/");
    const res = await page.request.get("/static/fonts/inter-latin-wght-normal.woff2");
    expect(res.ok()).toBeTruthy();
    expect(res.headers()["content-type"] ?? "").toMatch(/font|octet-stream/);

    const family = await page
      .locator("#home-title")
      .evaluate((el) => getComputedStyle(el).fontFamily);
    expect(family).toContain("Inter Variable");
    const loaded = await page.evaluate(() => document.fonts.check("400 16px 'Inter Variable'"));
    expect(loaded).toBe(true);

    expect(external).toEqual([]);
  });
});

test.describe("journey B — everyone's stats", () => {
  test("the front page carries global tiles and the board index reads its direction", async ({
    page,
  }) => {
    await page.goto("/");
    await expect(page.locator("#tile-boards")).toBeVisible();
    expect(Number(await page.locator("#tile-boards").getAttribute("data-value"))).toBeGreaterThan(0);
    expect(
      Number(await page.locator("#tile-placements").getAttribute("data-value")),
    ).toBeGreaterThan(0);

    await page.goto("/boards");
    const ascending = page.locator('#boards-index tr[data-ascending="true"]').first();
    await expect(ascending.locator("td.direction")).toContainText(/lowest wins/i);
  });

  test("a board offers its windows, and the window reaches the server", async ({ page }) => {
    await page.goto(`/boards/${LITHOBRAKE}`);
    const periods = page.locator("#board-periods a");
    await expect(periods).toHaveCount(5);
    await expect(page.locator('#board-periods a[data-period="alltime"]')).toHaveAttribute(
      "aria-current",
      "page",
    );

    await page.locator('#board-periods a[data-period="yearly"]').click();
    await expect(page).toHaveURL(/period=yearly/);
    await expect(page.locator('#board-periods a[data-period="yearly"]')).toHaveAttribute(
      "aria-current",
      "page",
    );
    // The window is named on the page, so a reader can tell which one they have.
    await expect(page.locator("#board-bucket")).toHaveAttribute("data-bucket", /^\d{4}$/);

    // A window this server does not serve is a 404, and says which of the two
    // things went wrong.
    const res = await page.goto(`/boards/${LITHOBRAKE}?period=fortnightly`);
    expect(res?.status()).toBe(404);
    await expect(page.locator("#not-found-detail")).toContainText(/window/i);
  });

  test("the pager counts ranks and disables what it cannot do", async ({ page }) => {
    await page.goto(`/boards/${LITHOBRAKE}`);
    await expect(page.locator("#board-range")).toContainText(/Ranks 1/);
    await expect(page.locator("#board-prev")).toHaveAttribute("aria-disabled", "true");
  });
});

test.describe("journey A — my own stats", () => {
  test("search finds a handle and the profile ranks it against the board", async ({ page }) => {
    await page.goto("/");
    await page.locator("#search-q").fill("demo");
    await page.locator("#search-q").press("Enter");

    await expect(page).toHaveURL(/\/search\?q=demo/);
    await expect(page.locator(`#search-results li[data-handle="${ME}"]`)).toBeVisible();

    // Two characters is the API's floor, and the UI's job is not to send the
    // request rather than to render the 400.
    await page.goto("/search?q=d");
    await expect(page.locator("#search-short")).toBeVisible();
    await expect(page.locator("#search-results")).toHaveCount(0);

    await page.goto("/search?q=zzzzzznotahandle");
    await expect(page.locator("#search-empty")).toContainText(/No handles match/i);

    await page.goto(`/p/${ME}`);
    const row = page.locator(`#profile-stats tr[data-stat="${LITHOBRAKE}"]`);
    await expect(row.locator("td.rank")).toContainText(/#1 of \d+/);
    // The board link lands on the page that contains this rank.
    await expect(row.locator("td a").first()).toHaveAttribute("href", /\/boards\//);
  });

  test("the search box suggests over the wire without a navigation", async ({ page }) => {
    await page.goto("/");
    await page.evaluate(() => {
      (window as unknown as Record<string, unknown>).__catlogNoReload = true;
    });
    await page.locator("#search-q").fill("demo");
    await expect(page.locator(`#search-suggest li a`, { hasText: ME })).toBeVisible({
      timeout: 15_000,
    });
    expect(
      await page.evaluate(
        () => (window as unknown as Record<string, unknown>).__catlogNoReload === true,
      ),
    ).toBe(true);
  });

  test('"this is me" persists, highlights my row, and can be taken back', async ({ page }) => {
    await page.goto(`/p/${ME}`);
    await expect(page.locator("#me-chip")).toBeHidden();

    await page.locator("#profile-me-toggle").click();
    await expect(page.locator("#me-chip")).toBeVisible();
    await expect(page.locator("#me-link")).toHaveText(ME);
    await expect(page.locator("#profile-me-note")).toContainText(/This is you/i);

    // It is a browser preference, not a login: it lives in localStorage under
    // one readable key and is never sent to catlog as an identifier.
    expect(await page.evaluate(() => localStorage.getItem("catlog:me"))).toBe(ME);

    // It survives a navigation and lights up the reader's own row.
    await page.goto(`/boards/${LITHOBRAKE}`);
    await expect(page.locator("tr.is-me")).toHaveAttribute("data-handle", ME);

    // And the front page leads with where they stand.
    await page.goto("/");
    await expect(page.locator("#me-standing")).toBeVisible({ timeout: 15_000 });
    await expect(page.locator("#me-standing-rows .standing-row").first()).toContainText(/#\d+ of/);

    // Taking it back clears the key rather than merely hiding the chip.
    await page.goto(`/p/${ME}`);
    await page.locator("#profile-me-toggle").click();
    await expect(page.locator("#me-chip")).toBeHidden();
    expect(await page.evaluate(() => localStorage.getItem("catlog:me"))).toBeNull();
  });

  test("a stored handle that stops resolving is reported, never guessed at and never cleared", async ({
    page,
  }) => {
    await page.goto("/");
    await page.evaluate(() => localStorage.setItem("catlog:me", "no_such_player_at_all"));
    await page.goto("/");

    const gone = page.locator("#me-gone");
    await expect(gone).toBeVisible({ timeout: 15_000 });
    await expect(gone).toContainText("no public profile");
    // catlog answers 404 identically for unknown, retired and banned, so the
    // copy must not imply which of the three it is.
    await expect(gone).not.toContainText(/banned|deleted|retired|renamed/i);
    // The stored value is the reader's data. It is still there.
    expect(await page.evaluate(() => localStorage.getItem("catlog:me"))).toBe(
      "no_such_player_at_all",
    );

    await gone.locator("button", { hasText: "Forget it" }).click();
    expect(await page.evaluate(() => localStorage.getItem("catlog:me"))).toBeNull();
  });
});

test.describe("journey C — against my friends", () => {
  test("three handles line up, the best cell is marked, and a gap stays a gap", async ({
    page,
  }) => {
    await page.goto(`/compare?handles=${ME},${FRIEND},${THIRD}`);
    await expect(page.locator("#compare-table")).toBeVisible();
    await expect(page.locator("#compare-table th.handle-col")).toHaveCount(3);

    // All three are on peak_g_survived, so exactly one of them is best on it —
    // and it is decided by the board's published direction, not by guessing.
    const peak = page.locator('#compare-table tr[data-stat="peak_g_survived"]');
    await expect(peak).toHaveAttribute("data-ascending", "false");
    await expect(peak.locator("td.best")).toHaveCount(1);
    await expect(peak.locator("td.best")).toHaveAttribute("data-handle", ME);
    // The rank is the world rank, not the rank among the compared handles.
    await expect(peak.locator("td.best")).toContainText(/#\d+ of \d+ worldwide/);

    // A board only one of the compared handles is on marks nothing: there is
    // nobody there to be better than.
    const litho = page.locator(`#compare-table tr[data-stat="${LITHOBRAKE}"]`);
    await expect(litho.locator("td.value")).toHaveCount(1);
    await expect(litho.locator("td.best")).toHaveCount(0);

    // An ascending board is marked the other way round: the smallest wins.
    const luna = page.locator('#compare-table tr[data-stat="fastest_to_luna"]');
    await expect(luna).toHaveAttribute("data-ascending", "true");
    const lunaValues = await luna
      .locator("td.value")
      .evaluateAll((els) => els.map((el) => Number((el as HTMLElement).dataset.value)));
    const lunaBest = Number(await luna.locator("td.best").getAttribute("data-value"));
    expect(lunaBest).toBe(Math.min(...lunaValues));

    // A board only some of them are on shows an em dash, not a zero.
    const absent = page.locator("#compare-table td.absent").first();
    if ((await absent.count()) > 0) {
      await expect(absent).toHaveAttribute("title", "not on this board");
      await expect(absent).toHaveText("—");
    }

    // A handle nobody holds is a column with a reason, not a silent omission.
    await page.goto(`/compare?handles=${ME},ghost_of_a_handle`);
    const ghost = page.locator('#compare-table th[data-handle="ghost_of_a_handle"]');
    await expect(ghost).toContainText(/no such player/i);
  });

  test("the comparison set is the URL, and it can be added to and taken from", async ({ page }) => {
    await page.goto(`/p/${ME}`);
    await page.locator("#profile-compare").click();
    await expect(page).toHaveURL(new RegExp(`/compare\\?handles=${ME}`));

    await page.locator("#compare-add-handle").fill(FRIEND);
    await page.locator("#compare-add button[type=submit]").click();
    // `?add=` merges and redirects, so the address bar is always the comparison.
    await expect(page).toHaveURL(/handles=/);
    await expect(page.locator("#compare-handles .chip")).toHaveCount(2);

    await page
      .locator(`#compare-handles .chip[data-handle="${FRIEND}"] .chip-remove`)
      .click();
    await expect(page.locator("#compare-handles .chip")).toHaveCount(1);

    await page.goto("/compare");
    await expect(page.locator("#compare-empty")).toBeVisible();
  });
});

test.describe("the raw event log", () => {
  test("it shows gameplay data, raw numbers and no tracking metadata", async ({ page }) => {
    await page.goto(`/p/${ME}`);
    await page.locator("#profile-events").click();
    await expect(page).toHaveURL(new RegExp(`/p/${ME}/events$`));

    const rows = page.locator("#events-log tr.event-row");
    expect(await rows.count()).toBeGreaterThan(0);

    // The payload is shown as the API sent it, unknown keys included.
    const first = rows.first();
    await first.locator("details.details-blob summary").click();
    await expect(first.locator("details.details-blob pre")).toBeVisible();

    // No payload on the page may carry an install-derived identifier or the
    // client's own clock. This is the one assertion here that is a privacy
    // property rather than a UI property.
    //
    // Scoped to the log rather than the page: the copy above the table explains
    // that the installation identifier is never published, and a substring match
    // over the prose would fail on the sentence that states the guarantee.
    for (const row of await page.locator("#events-log tbody").all()) {
      const text = (await row.innerText()).toLowerCase();
      expect(text).not.toContain("install");
      expect(text).not.toContain("wall_t");
      expect(text).not.toContain("user_key");
    }

    // Same fact through the JSON, where a key is a key rather than a word.
    const json = await (await page.request.get(`/v1/players/${ME}/events`)).json();
    const text = JSON.stringify(json);
    expect(text).not.toContain('"install"');
    expect(text).not.toContain('"wall_t"');
    expect(text).not.toContain('"user_key"');

    // A type filter narrows to types actually on the page.
    const chip = page.locator("#events-types a[data-type]").first();
    const type = await chip.getAttribute("data-type");
    await chip.click();
    await expect(page).toHaveURL(new RegExp(`type=${encodeURIComponent(type ?? "")}`));
    for (const row of await page.locator("#events-log tr.event-row").all()) {
      await expect(row).toHaveAttribute("data-type", type ?? "");
    }
  });

  test("paging is by cursor, and the end of the log says so", async ({ page }) => {
    await page.goto(`/p/${ME}/events`);
    await expect(page.locator("#events-newest")).toHaveAttribute("aria-disabled", "true");

    const older = page.locator("#events-older");
    // Either there is another page — in which case it is a cursor link, never an
    // offset — or the log ends here and the control says so rather than being a
    // dead link.
    if ((await older.getAttribute("href")) !== null) {
      await expect(older).toHaveAttribute("href", /before=\d+/);
      await older.click();
      await expect(page.locator("#events-newest")).toHaveAttribute("href", /\/events$/);
    } else {
      await expect(older).toHaveAttribute("aria-disabled", "true");
      await expect(older).toContainText(/End of the log/i);
    }
  });
});

test.describe("theme", () => {
  test("the toggle persists and the page never repaints from the wrong theme", async ({ page }) => {
    await page.goto("/");
    await page.locator("#theme-toggle").click();
    const first = await page.evaluate(() => localStorage.getItem("catlog:theme"));
    expect(first).toBe("light");
    await expect(page.locator("html")).toHaveAttribute("data-theme", "light");

    // The attribute is applied by a synchronous <head> script, so it is already
    // right on the very first frame after a reload rather than after a flash.
    await page.goto("/");
    await expect(page.locator("html")).toHaveAttribute("data-theme", "light");

    await page.locator("#theme-toggle").click();
    await expect(page.locator("html")).toHaveAttribute("data-theme", "dark");
    await page.locator("#theme-toggle").click();
    // "system" removes the attribute, so the media query wins again.
    await expect(page.locator("html")).not.toHaveAttribute("data-theme", /.*/);
    expect(await page.evaluate(() => localStorage.getItem("catlog:theme"))).toBeNull();
  });
});
