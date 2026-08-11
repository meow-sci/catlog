import { expect, test, type APIRequestContext } from "@playwright/test";

interface BadgeSummary {
  badge: string;
  title: string;
  blurb: string;
  group: string;
  holders: number;
}

interface SystemRef {
  hash: string;
  name: string;
  slug: string;
}

interface BadgeAward {
  badge: string;
  title: string;
  blurb: string;
  group: string;
  save?: number;
  system?: SystemRef;
  earned: number;
}

interface BadgeList {
  min_players: number;
  badges: BadgeSummary[];
}

interface BadgeHolders extends BadgeSummary {
  rows: Array<{
    rank: number;
    handle: string;
    save?: number;
    system?: SystemRef;
    earned: number;
  }>;
}

interface PlayerBadges {
  handle: string;
  earned: BadgeAward[];
  unearned: BadgeSummary[];
}

const FAMILY_PREFIXES = ["reached_", "orbited_", "landed_on_"] as const;

function isFamilyBadge(badge: string): boolean {
  return FAMILY_PREFIXES.some((prefix) => badge.startsWith(prefix));
}

async function publishedFamily(request: APIRequestContext): Promise<{
  badge: BadgeSummary;
  holders: BadgeHolders;
  representative: BadgeHolders["rows"][number] & { save: number; system: SystemRef };
}> {
  const listResponse = await request.get("/v1/badges");
  expect(listResponse.ok()).toBeTruthy();
  const list = (await listResponse.json()) as BadgeList;

  for (const badge of list.badges.filter((entry) => isFamilyBadge(entry.badge))) {
    const holdersResponse = await request.get(`/v1/badges/${badge.badge}`);
    expect(holdersResponse.ok()).toBeTruthy();
    const holders = (await holdersResponse.json()) as BadgeHolders;
    const representative = holders.rows.find(
      (row): row is typeof row & { save: number; system: SystemRef } =>
        row.save !== undefined && row.system !== undefined,
    );
    if (representative) return { badge, holders, representative };
  }

  throw new Error("the demo seed has no published family badge with save and system provenance");
}

test.describe("badges", () => {
  test("the catalogue matches the published API row for row", async ({ page }) => {
    const response = await page.request.get("/v1/badges");
    expect(response.ok()).toBeTruthy();
    const json = (await response.json()) as BadgeList;

    await page.goto("/badges");
    await expect(page.locator("#badges-title")).toBeVisible();
    await expect(page.locator("#badges-catalogue")).toHaveAttribute(
      "data-min-players",
      String(json.min_players),
    );

    const rendered = await page.locator("#badges-catalogue .badge-tile").evaluateAll((tiles) =>
      tiles.map((tile) => ({
        badge: (tile as HTMLElement).dataset.badge,
        title: tile.querySelector("h3 a")?.textContent?.trim(),
        blurb: tile.querySelector("p")?.textContent?.trim(),
        group: (tile.closest(".badge-group") as HTMLElement | null)?.dataset.group,
        holders: Number((tile.querySelector(".badge-count") as HTMLElement | null)?.dataset.value),
        href: tile.querySelector("h3 a")?.getAttribute("href"),
      })),
    );
    expect(rendered).toEqual(
      json.badges.map((badge) => ({
        badge: badge.badge,
        title: badge.title,
        blurb: badge.blurb,
        group: badge.group,
        holders: badge.holders,
        href: `/badges/${badge.badge}`,
      })),
    );

    // There is deliberately no fixed catalogue-size assertion: family members
    // appear as soon as enough players encounter a body the server never named.
    const family = json.badges.find((badge) => isFamilyBadge(badge.badge));
    expect(family).toBeDefined();
    expect(family?.holders).toBeGreaterThanOrEqual(json.min_players);
  });

  test("a published family badge ranks its holders earliest first", async ({ page }) => {
    const { badge, holders, representative } = await publishedFamily(page.request);
    expect(holders.rows.length).toBeGreaterThan(1);
    expect(holders.rows.map((row) => row.earned)).toEqual(
      [...holders.rows].map((row) => row.earned).sort((a, b) => a - b),
    );

    await page.goto(`/badges/${badge.badge}`);
    await expect(page.locator("#badge-title")).toHaveAttribute("data-badge", badge.badge);

    const rendered = await page.locator("#badge-holders tr.badge-holder").evaluateAll((rows) =>
      rows.map((row) => ({
        rank: Number(row.getAttribute("data-rank")),
        handle: row.getAttribute("data-handle"),
        earned: Number((row.querySelector("td.when") as HTMLElement | null)?.dataset.value),
      })),
    );
    expect(rendered).toEqual(
      holders.rows.map((row) => ({
        rank: row.rank,
        handle: row.handle,
        earned: row.earned,
      })),
    );

    const holder = page.locator(
      `#badge-holders tr.badge-holder[data-handle="${representative.handle}"]`,
    );
    await expect(holder.locator("td.save a")).toHaveText(`Save ${representative.save}`);
    await expect(holder.locator("td.save a")).toHaveAttribute(
      "href",
      `/p/${representative.handle}/saves/${representative.save}`,
    );
    await expect(holder.locator("td.system a")).toHaveText(representative.system.name);
    await expect(holder.locator("td.system a")).toHaveAttribute(
      "href",
      `/systems/${representative.system.slug}`,
    );
  });

  test("player and save pages separate their checklists and preserve provenance", async ({
    page,
  }) => {
    const { representative } = await publishedFamily(page.request);
    const playerResponse = await page.request.get(`/v1/players/${representative.handle}/badges`);
    expect(playerResponse.ok()).toBeTruthy();
    const playerBadges = (await playerResponse.json()) as PlayerBadges;
    expect(playerBadges.earned.length).toBeGreaterThan(0);
    expect(playerBadges.unearned.length).toBeGreaterThan(0);

    await page.goto(`/p/${representative.handle}/badges`);
    await expect(page.locator("#player-badges-title")).toHaveAttribute(
      "data-handle",
      representative.handle,
    );
    const earned = await page
      .locator("#earned-badges .badge-earned")
      .evaluateAll((tiles) => tiles.map((tile) => (tile as HTMLElement).dataset.badge));
    const unearned = await page
      .locator("#unearned-badges .badge-unearned")
      .evaluateAll((tiles) => tiles.map((tile) => (tile as HTMLElement).dataset.badge));
    expect(earned).toEqual(playerBadges.earned.map((badge) => badge.badge));
    expect(unearned).toEqual(playerBadges.unearned.map((badge) => badge.badge));
    expect(earned.filter((badge) => unearned.includes(badge))).toEqual([]);

    const lifetimeAward = playerBadges.earned.find(
      (award) =>
        award.save === representative.save && award.system?.slug === representative.system.slug,
    );
    expect(lifetimeAward).toBeDefined();
    if (!lifetimeAward) throw new Error("the family holder is missing from the lifetime checklist");
    const lifetimeTile = page.locator(
      `#earned-badges .badge-earned[data-badge="${lifetimeAward.badge}"]`,
    );
    await expect(lifetimeTile.locator(".badge-provenance")).toContainText(
      `Save ${representative.save}`,
    );
    await expect(lifetimeTile.locator(".badge-provenance")).toContainText(
      representative.system.name,
    );
    await expect(lifetimeTile.locator(".badge-provenance a").first()).toHaveAttribute(
      "href",
      `/p/${representative.handle}/saves/${representative.save}`,
    );
    await expect(lifetimeTile.locator(".badge-provenance a").last()).toHaveAttribute(
      "href",
      `/systems/${representative.system.slug}`,
    );

    const saveResponse = await page.request.get(
      `/v1/players/${representative.handle}/saves/${representative.save}/badges`,
    );
    expect(saveResponse.ok()).toBeTruthy();
    const saveBadges = (await saveResponse.json()) as PlayerBadges;
    const lifetimeSet = new Set(playerBadges.earned.map((badge) => badge.badge));
    expect(saveBadges.earned.every((badge) => lifetimeSet.has(badge.badge))).toBeTruthy();

    await page.goto(`/p/${representative.handle}/saves/${representative.save}/badges`);
    await expect(page.locator("#player-badges-title")).toHaveAttribute(
      "data-save",
      String(representative.save),
    );
    const renderedSaveEarned = await page
      .locator("#earned-badges .badge-earned")
      .evaluateAll((tiles) => tiles.map((tile) => (tile as HTMLElement).dataset.badge));
    const renderedSaveUnearned = await page
      .locator("#unearned-badges .badge-unearned")
      .evaluateAll((tiles) => tiles.map((tile) => (tile as HTMLElement).dataset.badge));
    expect(renderedSaveEarned).toEqual(saveBadges.earned.map((badge) => badge.badge));
    expect(renderedSaveUnearned).toEqual(saveBadges.unearned.map((badge) => badge.badge));
  });
});
