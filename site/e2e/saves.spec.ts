import { expect, test } from "@playwright/test";
import { admin } from "./helpers";

interface SaveSummary {
  save: number;
  system?: { name: string; slug: string };
  playtime_ms: number;
  first: number;
  last: number;
  boards: number;
}

interface SaveDetail {
  save: number;
  system?: { name: string; slug: string };
  system_changed?: boolean;
  playtime_ms: number;
  rewound?: boolean;
  stats: Array<{
    stat: string;
    title: string;
    value: number;
    rank: number;
    entrants: number;
    context?: Record<string, unknown>;
  }>;
}

test.describe("saves", () => {
  test("the seeded player lists populated saves with friendly and unknown systems", async ({
    page,
  }) => {
    const api = await page.request.get("/v1/players/demo_ace/saves");
    expect(api.ok()).toBeTruthy();
    const json = (await api.json()) as { handle: string; saves: SaveSummary[] };
    expect(json.handle).toBe("demo_ace");
    expect(json.saves.length).toBeGreaterThanOrEqual(2);
    const firstSeeded = json.saves.find((save) => save.save === 1);
    const secondSeeded = json.saves.find((save) => save.save === 2);
    expect(firstSeeded).toBeDefined();
    expect(secondSeeded).toBeDefined();
    if (!firstSeeded || !secondSeeded) throw new Error("seeded saves 1 and 2 are missing");
    expect(firstSeeded.system).toBeUndefined();
    expect(firstSeeded.boards).toBeGreaterThan(0);
    expect(secondSeeded.system).toMatchObject({ name: "Sol", slug: "sol" });
    expect(secondSeeded.boards).toBeGreaterThan(0);

    const response = await page.goto("/p/demo_ace/saves");
    expect(response?.status()).toBe(200);
    await expect(page.locator("#saves-title")).toHaveAttribute("data-handle", "demo_ace");
    const rows = page.locator("#saves-table tr.save-row");
    await expect(rows).toHaveCount(json.saves.length);

    for (const [index, save] of json.saves.entries()) {
      const row = rows.nth(index);
      await expect(row).toHaveAttribute("data-save", String(save.save));
      await expect(row.locator("td.save a")).toHaveAttribute(
        "href",
        `/p/demo_ace/saves/${save.save}`,
      );
      await expect(row.locator("td.value").first()).toHaveAttribute(
        "data-value",
        String(save.playtime_ms),
      );
      await expect(row.locator("td.value").last()).toHaveAttribute(
        "data-value",
        String(save.boards),
      );
      await expect(row.locator("time").first()).toHaveAttribute(
        "datetime",
        new Date(Math.floor(save.first / 1000) * 1000).toISOString().replace(".000Z", "Z"),
      );
      await expect(row.locator("time").last()).toHaveAttribute(
        "datetime",
        new Date(Math.floor(save.last / 1000) * 1000).toISOString().replace(".000Z", "Z"),
      );
    }

    await expect(rows.first().locator("td.system")).toHaveText("—");
    await expect(rows.first().locator("td.system a")).toHaveCount(0);
    await expect(rows.nth(1).locator("td.system a")).toHaveText("Sol");
    await expect(rows.nth(1).locator("td.system a")).toHaveAttribute("href", "/systems/sol");
    await expect(page.getByText("Badges", { exact: true })).toHaveCount(0);
  });

  test("a player with no telemetry has an honest empty saves page", async ({ page, request }) => {
    await admin(request, "/admin/issue", { handle: "e2e_empty_saves" });
    const response = await page.goto("/p/e2e_empty_saves/saves");
    expect(response?.status()).toBe(200);
    await expect(page.locator("#saves-empty")).toHaveText("No saves recorded yet.");
    await expect(page.locator("#saves-table tr.save-row")).toHaveCount(0);
    await expect(page.getByText("Badges", { exact: true })).toHaveCount(0);
  });

  test("save detail agrees with the API and carries ranking and provenance", async ({ page }) => {
    const api = await page.request.get("/v1/players/demo_ace/saves/2");
    expect(api.ok()).toBeTruthy();
    const json = (await api.json()) as SaveDetail;
    expect(json.system).toMatchObject({ name: "Sol", slug: "sol" });
    expect(json.system_changed).toBe(true);
    expect(json.rewound).toBe(true);
    expect(json.stats.length).toBeGreaterThan(0);

    const response = await page.goto("/p/demo_ace/saves/2");
    expect(response?.status()).toBe(200);
    await expect(page.locator("#save-title")).toHaveText("Save 2");
    await expect(page.locator("#save-summary a")).toHaveText("Sol");
    await expect(page.locator("#save-summary a")).toHaveAttribute("href", "/systems/sol");
    await expect(page.locator("#save-summary .system-changed")).toHaveAttribute(
      "title",
      "The celestial system this save is in changed. Per-system comparisons before and after are not comparing the same worlds.",
    );

    const rows = page.locator("#save-stats tr.profile-row");
    await expect(rows).toHaveCount(json.stats.length);
    for (const stat of json.stats) {
      const row = rows.filter({ has: page.locator(`a[href="/boards/${stat.stat}?scope=career"]`) });
      await expect(row).toHaveCount(1);
      await expect(row).toHaveAttribute("data-rank", String(stat.rank));
      await expect(row.locator("td.save-placement")).toContainText(
        `#${stat.rank} of ${stat.entrants} saves on ${stat.title}`,
      );
      await expect(row.locator("td.value")).toHaveAttribute("data-value", String(stat.value));
      await expect(row.locator("td.value .rewound")).toHaveAttribute(
        "title",
        "An earlier save of this career was loaded, so its clock did not only run forwards.",
      );
    }

    const landing = json.stats.find((stat) => stat.stat === "softest_landing");
    expect(landing).toBeDefined();
    expect(landing?.context?.body).toBe("mars");
    await expect(
      rows.filter({ has: page.locator('a[href="/boards/softest_landing?scope=career"]') }),
    ).toContainText("Mars");
    await expect(page.getByText("Badges", { exact: true })).toHaveCount(0);

    await page.goto("/p/demo_ace/saves/1");
    await expect(page.locator("#save-summary a[href^='/systems/']")).toHaveCount(0);
    await expect(page.locator(".system-changed")).toHaveCount(0);
    await expect(page.locator(".rewound")).toHaveCount(0);
    await expect(page.locator('a[href="/p/demo_ace/saves"]')).toBeVisible();
  });
});
