import { expect, test } from "@playwright/test";
import { pushEvent, ulid } from "./helpers";

/**
 * The raw event pages: the global log at /events, the per-handle log at
 * /p/{handle}/events, and the datastar live tail they share.
 *
 * Rows on both pages come from one `event-row` partial, and the tail's prime
 * stamps `data-source="sse"` on `#events-body` — the same readiness signal the
 * feed spec waits on, and the only way to tell an open stream from a page whose
 * datastar module never ran.
 */

test.describe("the global raw event log", () => {
  test("renders the log with chips, pager and nav entry", async ({ page }) => {
    await page.goto("/events");

    await expect(page.locator("#nav-events")).toHaveAttribute("aria-current", "page");
    await expect(page.locator("#events-log")).toBeVisible();

    // Every row names its player as a profile link and carries its seq.
    const row = page.locator("#events-body tr.event-row").first();
    await expect(row).toHaveAttribute("data-seq", /^\d+$/);
    await expect(row.locator("td.handle a")).toHaveAttribute("href", /^\/p\//);

    // The received column is a machine-readable instant.
    await expect(row.locator("td.recv time")).toHaveAttribute(
      "datetime",
      /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$/,
    );

    // The type chips are the taxonomy union; "All types" leads the row.
    await expect(page.locator("#events-types a.chip").first()).toHaveText("All types");
    expect(await page.locator("#events-types a[data-type]").count()).toBeGreaterThan(0);

    // Page one: newest is disabled, and the pager pages by cursor, not offset.
    await expect(page.locator("#events-newest")).toHaveAttribute("aria-disabled", "true");
  });

  test("?handle= narrows the log and offers a way out", async ({ page }) => {
    await page.goto("/events?handle=demo_crasher");

    await expect(page.locator("#events-handle-filter")).toHaveAttribute(
      "data-handle",
      "demo_crasher",
    );
    await expect(page.locator("#events-handle-clear")).toHaveAttribute("href", "/events");

    // Every visible row belongs to the filtered handle.
    for (const href of await page
      .locator("#events-body tr.event-row td.handle a")
      .evaluateAll((links) => links.map((a) => a.getAttribute("href")))) {
      expect(href).toBe("/p/demo_crasher");
    }

    // An unknown handle is the same one answer as everywhere else.
    const gone = await page.goto("/events?handle=nobody_here");
    expect(gone?.status()).toBe(404);
    await expect(page.locator("#not-found")).toBeVisible();
  });

  test("a new event appears over SSE with the arrival mark, without a reload", async ({
    page,
    request,
  }) => {
    await page.goto("/events");

    // The tail's prime replaces the tbody with an identical copy marked "sse".
    await expect(page.locator("#events-body")).toHaveAttribute("data-source", "sse", {
      timeout: 15_000,
    });

    await page.evaluate(() => {
      (window as unknown as Record<string, unknown>).__catlogNoReload = true;
    });

    const marker = `e2e-rawlog-${Date.now().toString(36)}`;
    await pushEvent(
      request,
      "demo_ace",
      "vehicle.rud",
      {
        cause: "collision",
        peak_g: 9.5,
        peak_q_pa: 30000,
        speed_ms: 123,
        altitude_m: 0,
        body: marker,
        crew_count: 1,
      },
      { flight: ulid(), session: ulid() },
    );

    const row = page.locator("#events-body tr.event-row", { hasText: marker });
    await expect(row).toBeVisible({ timeout: 15_000 });
    // Only the streamed row is marked arrived — the flash is scoped to it.
    await expect(row).toHaveAttribute("data-arrived", "");
    await expect(row).toHaveAttribute("data-type", "vehicle.rud");
    await expect(row.locator("td.handle a")).toHaveText("demo_ace");

    // Prepended: the newest row is first.
    await expect(page.locator("#events-body tr.event-row").first()).toContainText(marker);

    // Nothing navigated.
    expect(
      await page.evaluate(
        () => (window as unknown as Record<string, unknown>).__catlogNoReload === true,
      ),
    ).toBe(true);
  });

  test("pausing the tail closes the stream; resuming re-primes it", async ({ page, request }) => {
    await page.goto("/events");
    await expect(page.locator("#events-body")).toHaveAttribute("data-source", "sse", {
      timeout: 15_000,
    });

    // me.js unhides the control once it is wired; pausing closes the SSE by
    // removing `data-init` (requestCancellation: 'cleanup' registered the
    // abort), so paused means closed, not buffered.
    const live = page.locator("#events-live");
    await expect(live).toBeVisible();
    await live.click();
    await expect(page.locator("#events-panel")).toHaveAttribute("data-stream", "paused");
    await expect(live).toHaveAttribute("aria-pressed", "false");

    // An event pushed while paused must not appear...
    const marker = `e2e-paused-${Date.now().toString(36)}`;
    await pushEvent(
      request,
      "demo_ace",
      "vehicle.soi",
      { from_body: "kerbin", to_body: marker },
      { flight: ulid(), session: ulid() },
    );
    await expect(page.locator("#events-body tr.event-row", { hasText: marker })).toHaveCount(0);

    // ...until resume reconnects and re-primes, which heals the gap: the row
    // arrives as part of the fresh prime, so it carries no arrival mark.
    await live.click();
    await expect(live).toHaveAttribute("aria-pressed", "true");
    const row = page.locator("#events-body tr.event-row", { hasText: marker });
    await expect(row).toBeVisible({ timeout: 15_000 });
    await expect(row).not.toHaveAttribute("data-arrived", "");
  });

  test("a deep page is historical: no tail, no live control", async ({ page }) => {
    await page.goto("/events");
    // Any cursor makes the page historical; the seeded log is deep enough that
    // page one always carries an "older" link.
    const older = page.locator("a#events-older");
    await expect(older).toBeVisible();
    await older.click();

    await expect(page.locator("#events-log")).toBeVisible();
    await expect(page.locator("#events-tail")).toHaveCount(0);
    await expect(page.locator("#events-live")).toHaveCount(0);
    // And a way back to the top of the log.
    await expect(page.locator("a#events-newest")).toBeVisible();
  });
});

test.describe("the per-handle raw event log", () => {
  test("page one tails its own handle over the shared stream", async ({ page, request }) => {
    await page.goto("/p/demo_ace/events");

    await expect(page.locator("#events-title")).toHaveAttribute("data-handle", "demo_ace");
    await expect(page.locator("#events-tail")).toHaveAttribute(
      "data-init",
      /\/v1\/events\/sse\?handle=demo_ace/,
    );
    await expect(page.locator("#events-body")).toHaveAttribute("data-source", "sse", {
      timeout: 15_000,
    });

    // demo_crasher's events never reach demo_ace's tail; demo_ace's do. The
    // sync point is demo_ace's own later event — by the time it shows, the
    // other player's had its chance to have been (wrongly) patched in.
    const wrong = `e2e-other-${Date.now().toString(36)}`;
    const mine = `e2e-mine-${Date.now().toString(36)}`;
    await pushEvent(
      request,
      "demo_crasher",
      "vehicle.soi",
      { from_body: "kerbin", to_body: wrong },
      { flight: ulid(), session: ulid() },
    );
    await pushEvent(
      request,
      "demo_ace",
      "vehicle.soi",
      { from_body: "kerbin", to_body: mine },
      { flight: ulid(), session: ulid() },
    );

    const row = page.locator("#events-body tr.event-row", { hasText: mine });
    await expect(row).toBeVisible({ timeout: 15_000 });
    await expect(row).toHaveAttribute("data-arrived", "");
    await expect(page.locator("#events-body tr.event-row", { hasText: wrong })).toHaveCount(0);
  });

  test("rows render byte-identically to the global page", async ({ page }) => {
    // The shared partial is asserted properly server-side; here, spot-check
    // that the same event's row on both pages carries the same cells.
    await page.goto("/p/demo_ace/events");
    const seq = await page
      .locator("#events-body tr.event-row")
      .first()
      .getAttribute("data-seq");
    expect(seq).toMatch(/^\d+$/);
    const perHandle = await page.locator(`#event-row-${seq}`).innerHTML();

    await page.goto(`/events?handle=demo_ace`);
    const global = await page.locator(`#event-row-${seq}`).innerHTML();
    expect(global).toBe(perHandle);
  });
});
