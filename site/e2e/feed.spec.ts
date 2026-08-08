import { expect, test } from "@playwright/test";
import { pushEvent, ulid } from "./helpers";

/**
 * §8 spec 4 — the datastar SSE feed.
 *
 * Open the home page, push a real event through the admin API, and assert the
 * line appears **without a reload**. The no-reload half is asserted rather than
 * assumed: a marker is planted on `window` before the event is pushed, and it
 * has to still be there afterwards. A navigation would have wiped it, so if the
 * marker survives and the line is on the page, the only thing that could have
 * put it there is the SSE stream.
 */

test.describe("the live feed", () => {
  test("a new event appears over SSE without a reload", async ({ page, request }) => {
    await page.goto("/");
    await expect(page.locator("#feed-panel")).toBeVisible();

    // Wait for datastar to boot and the stream to prime: the server stamps
    // data-source="sse" on the list it patches in, so this is a real "the
    // stream is open" signal rather than "the page rendered".
    await expect(page.locator("#feed")).toHaveAttribute("data-source", "sse", { timeout: 15_000 });

    await page.evaluate(() => {
      (window as unknown as Record<string, unknown>).__catlogNoReload = true;
    });

    // A body name nothing else in the system uses, so the assertion cannot pass
    // on a pre-existing line. `place()` keeps it verbatim (§5.6 feed summaries).
    const marker = `e2e-feed-${Date.now().toString(36)}`;
    const flight = ulid();
    await pushEvent(
      request,
      "demo_ace",
      "vehicle.rud",
      {
        cause: "collision",
        peak_g: 12.5,
        peak_q_pa: 40000,
        speed_ms: 321,
        altitude_m: 0,
        body: marker,
        crew_count: 1,
      },
      { flight, session: ulid() },
    );

    const line = page.locator(`#feed li.feed-item`, { hasText: marker });
    await expect(line).toBeVisible({ timeout: 15_000 });
    await expect(line).toHaveAttribute("data-type", "vehicle.rud");
    await expect(line).toContainText("demo_ace");
    await expect(line).toContainText("a collision");
    // Only the streamed line is marked arrived — the arrival flash is scoped to
    // it, so primed rows never animate on load or reconnect.
    await expect(line).toHaveAttribute("data-arrived", "");
    // The summary's leading handle is a profile link.
    await expect(line.locator(`a[href="/p/demo_ace"]`)).toHaveText("demo_ace");

    // The newest line is prepended, so it is first.
    await expect(page.locator("#feed li.feed-item").first()).toContainText(marker);

    // Nothing navigated.
    expect(
      await page.evaluate(
        () => (window as unknown as Record<string, unknown>).__catlogNoReload === true,
      ),
    ).toBe(true);
  });

  test("a flagged flight never reaches the feed", async ({ page, request }) => {
    await page.goto("/");
    await expect(page.locator("#feed")).toHaveAttribute("data-source", "sse", { timeout: 15_000 });

    const marker = `e2e-flagged-${Date.now().toString(36)}`;
    const flight = ulid();
    const session = ulid();

    // Flag the flight first, then give it something spectacular to report.
    await pushEvent(request, "demo_ace", "flight.flagged",
      { flag: "teleport", detail: "e2e" }, { flight, session });
    await pushEvent(
      request,
      "demo_ace",
      "vehicle.impact",
      { speed_ms: 9999, energy_j: 1e12, survived: true, launch_pad: false, body: marker, crew_count: 2 },
      { flight, session },
    );

    // Push an unflagged event afterwards and wait for *that* to arrive: it is
    // the sync point that proves the flagged one had its chance and was dropped,
    // rather than merely not having arrived yet.
    const sentinel = `e2e-sentinel-${Date.now().toString(36)}`;
    await pushEvent(
      request,
      "demo_ace",
      "vehicle.soi",
      { from_body: "kerbin", to_body: sentinel },
      { flight: ulid(), session },
    );

    await expect(page.locator("#feed li.feed-item", { hasText: sentinel })).toBeVisible({ timeout: 15_000 });
    await expect(page.locator("#feed li.feed-item", { hasText: marker })).toHaveCount(0);

    // And it is on no board either.
    const board = await page.request.get("/v1/leaderboards/biggest_lithobrake_survived");
    const json = await board.json();
    expect(json.rows.every((r: { value: number }) => r.value !== 9999)).toBe(true);
  });

  test("the feed stream is never cached", async ({ page }) => {
    // §4.8's cache header is on every read endpoint except this one — a cached
    // event stream is a stream that never updates.
    //
    // Done through the page rather than the request fixture because that fixture
    // reads the whole body, and this body never ends. `fetch` resolves as soon
    // as the headers arrive, which is all that is being asserted.
    await page.goto("/");
    const headers = await page.evaluate(async () => {
      const controller = new AbortController();
      const res = await fetch("/v1/feed/sse", { signal: controller.signal });
      const out = {
        contentType: res.headers.get("content-type") ?? "",
        cacheControl: res.headers.get("cache-control") ?? "",
      };
      controller.abort();
      return out;
    });
    expect(headers.contentType).toContain("text/event-stream");
    expect(headers.cacheControl).not.toContain("s-maxage");
    expect(headers.cacheControl).toContain("no-cache");
  });
});
