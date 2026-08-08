import { expect, test } from "@playwright/test";

/**
 * /stats — the stats of the stats, and the one page that describes catlog
 * rather than its players.
 *
 * Worth an end-to-end check rather than a unit test because the page's numbers
 * come from a projection: this proves the fold ran against a real log, not that
 * a template renders a struct. The locale half of what it renders is asserted
 * in reader.spec.ts, with the rest of the number rules.
 */

test.describe("the stats page", () => {
  test("renders the collection census from the folded log", async ({ page }) => {
    await page.goto("/stats");

    await expect(page.locator("#nav-stats")).toHaveAttribute("aria-current", "page");
    await expect(page.locator("#stats-title")).toBeVisible();

    // Read the figure out of `data-value`, never out of the rendered text: the
    // text is localised in the browser and is not a number this file can parse.
    const total = Number(await page.locator("#tile-events").getAttribute("data-value"));
    expect(total).toBeGreaterThan(0);

    // The four rolling windows, each naming the bucket the *server's* clock is
    // in — which is why the assertion is on the shape rather than on a date
    // this process could compute for itself and get wrong by a timezone.
    for (const [period, shape] of [
      ["daily", /^\d{4}-\d{2}-\d{2}$/],
      ["weekly", /^\d{4}-W\d{2}$/],
      ["monthly", /^\d{4}-\d{2}$/],
      ["yearly", /^\d{4}$/],
    ] as const) {
      await expect(page.locator(`[data-period="${period}"]`)).toHaveAttribute("data-bucket", shape);
    }

    // Every type the seeded log carries, and the projector's own cursor.
    expect(await page.locator("#stats-types tr[data-type]").count()).toBeGreaterThan(0);
    await expect(page.locator('tr[data-census="Projector lag"] td.value')).toHaveAttribute(
      "data-value",
      /^\d+$/,
    );

    // It is about the collection, not a leaderboard: no handle appears on it.
    expect(await page.locator("a[href^='/p/']").count()).toBe(0);

    // The HTML agrees with the JSON it is rendered from.
    const json = (await (await page.request.get("/v1/stats")).json()) as {
      events: { total: number };
    };
    expect(json.events.total).toBe(total);
  });
});
