import { expect, test, type Page } from "@playwright/test";
import { admin } from "./helpers";

interface ChallengeSummary {
  challenge: string;
  title: string;
  blurb: string;
  unit: string;
  ascending: boolean;
  scope: "player" | "career" | "system";
  opens: number;
  closes: number;
  state: "upcoming" | "open" | "closed";
  entrants: number;
}

interface ChallengeList {
  now: number;
  challenges: ChallengeSummary[];
}

interface ChallengeRow {
  rank: number;
  handle: string;
  save?: number;
  system?: { name: string; slug: string };
  value: number;
  updated: number;
}

interface ChallengeDetail extends ChallengeSummary {
  rows: ChallengeRow[];
}

interface ClockResponse {
  now_ms: number;
  controllable: boolean;
}

const WEEK_33_OPEN = 1_786_665_600_000;
const WEEK_33_CLOSED = 1_786_924_800_000;
const STATES = ["open", "upcoming", "closed"] as const;

async function challengeList(page: Page): Promise<ChallengeList> {
  const response = await page.request.get("/v1/challenges");
  expect(response.ok()).toBeTruthy();
  return (await response.json()) as ChallengeList;
}

test.describe("challenges", () => {
  test("open Week 33 rankings become a retained finished archive", async ({ page, request }) => {
    try {
      const openList = await challengeList(page);
      expect(openList.challenges.length).toBeGreaterThan(0);
      expect(openList.challenges.every((challenge) => challenge.state === "open")).toBeTruthy();

      await page.goto("/challenges");
      await expect(page.locator("#challenges-title")).toBeVisible();
      const groupOrder = await page
        .locator("#challenges-index .challenge-group")
        .evaluateAll((groups) => groups.map((group) => (group as HTMLElement).dataset.state));
      expect(groupOrder).toEqual(STATES);

      const renderedIndex = await page
        .locator("#challenges-index tr.challenge-row")
        .evaluateAll((rows) =>
          rows.map((row) => ({
            challenge: (row as HTMLElement).dataset.challenge,
            state: (row as HTMLElement).dataset.state,
            group: (row.closest(".challenge-group") as HTMLElement | null)?.dataset.state,
            title: row.querySelector("th a")?.textContent?.trim(),
            href: row.querySelector("th a")?.getAttribute("href"),
            entrants: Number((row.querySelector("td.value") as HTMLElement | null)?.dataset.value),
          })),
        );
      expect(renderedIndex).toEqual(
        openList.challenges.map((challenge) => ({
          challenge: challenge.challenge,
          state: challenge.state,
          group: challenge.state,
          title: challenge.title,
          href: `/challenges/${challenge.challenge}`,
          entrants: challenge.entrants,
        })),
      );
      const renderedNow = Number(await page.locator("#challenges-index").getAttribute("data-now"));
      expect(renderedNow).toBeGreaterThanOrEqual(WEEK_33_OPEN);
      expect(renderedNow).toBeLessThan(WEEK_33_CLOSED);

      const openChallenge = openList.challenges.find(
        (challenge) => challenge.scope !== "player" && challenge.entrants > 0,
      );
      expect(openChallenge).toBeDefined();
      if (!openChallenge) throw new Error("the demo seed has no ranked scoped challenge");

      const detailResponse = await page.request.get(`/v1/challenges/${openChallenge.challenge}`);
      expect(detailResponse.ok()).toBeTruthy();
      const openDetail = (await detailResponse.json()) as ChallengeDetail;
      expect(openDetail.state).toBe("open");
      expect(openDetail.rows.length).toBeGreaterThan(0);

      await page.goto(`/challenges/${openChallenge.challenge}`);
      await expect(page.locator("#challenge-title")).toHaveAttribute(
        "data-challenge",
        openChallenge.challenge,
      );
      await expect(page.locator("#challenge-metadata")).toHaveAttribute("data-state", "open");
      await expect(page.locator("#challenge-deadline")).toBeVisible();
      const openRows = await page
        .locator("#challenge-standings tr.challenge-holder")
        .evaluateAll((rows) =>
          rows.map((row) => ({
            rank: Number(row.getAttribute("data-rank")),
            handle: row.getAttribute("data-handle"),
            value: Number((row.querySelector("td.value") as HTMLElement | null)?.dataset.value),
            updated: Number((row.querySelector("td.when") as HTMLElement | null)?.dataset.value),
          })),
        );
      expect(openRows).toEqual(
        openDetail.rows.map((row) => ({
          rank: row.rank,
          handle: row.handle,
          value: row.value,
          updated: row.updated,
        })),
      );
      expect(openRows.map((row) => row.rank)).toEqual(
        Array.from({ length: openRows.length }, (_, index) => index + 1),
      );

      const representative = openDetail.rows[0];
      if (!representative) throw new Error("the scoped challenge has no visible ranked row");
      const representativeRow = page.locator(
        `#challenge-standings tr.challenge-holder[data-rank="${representative.rank}"][data-handle="${representative.handle}"]`,
      );
      if (openChallenge.scope === "system") {
        expect(representative.system).toBeDefined();
        if (!representative.system)
          throw new Error("system challenge row has no system provenance");
        await expect(representativeRow.locator("td.system a")).toHaveText(
          representative.system.name,
        );
        await expect(representativeRow.locator("td.system a")).toHaveAttribute(
          "href",
          `/systems/${representative.system.slug}`,
        );
      } else {
        expect(representative.save).toBeDefined();
        if (representative.save === undefined) {
          throw new Error("career challenge row has no save provenance");
        }
        await expect(representativeRow.locator("td.save a")).toHaveText(
          `Save ${representative.save}`,
        );
        await expect(representativeRow.locator("td.save a")).toHaveAttribute(
          "data-value",
          String(representative.save),
        );
        await expect(representativeRow.locator("td.save a")).toHaveAttribute(
          "href",
          `/p/${representative.handle}/saves/${representative.save}`,
        );
      }

      await page.goto("/");
      await expect(page.locator("#open-challenge")).toHaveAttribute(
        "data-challenge",
        openList.challenges[0].challenge,
      );
      await expect(page.locator("#open-challenge table.catlog-board")).toHaveAttribute(
        "data-stat",
        openList.challenges[0].challenge,
      );
      const homeDetailResponse = await page.request.get(
        `/v1/challenges/${openList.challenges[0].challenge}`,
      );
      expect(homeDetailResponse.ok()).toBeTruthy();
      const homeDetail = (await homeDetailResponse.json()) as ChallengeDetail;
      const homeRows = await page.locator("#open-challenge tr.board-row").evaluateAll((rows) =>
        rows.map((row) => ({
          rank: Number(row.getAttribute("data-rank")),
          handle: row.getAttribute("data-handle"),
          value: Number((row.querySelector("td.value") as HTMLElement | null)?.dataset.value),
        })),
      );
      expect(homeRows).toEqual(
        homeDetail.rows.slice(0, homeRows.length).map((row) => ({
          rank: row.rank,
          handle: row.handle,
          value: row.value,
        })),
      );

      const moved = (await admin(request, "/admin/clock", {
        at_ms: WEEK_33_CLOSED,
      })) as ClockResponse;
      expect(moved.controllable).toBe(true);
      expect(moved.now_ms).toBeGreaterThanOrEqual(WEEK_33_CLOSED);

      const closedList = await challengeList(page);
      expect(closedList.challenges.map((challenge) => challenge.challenge)).toEqual(
        openList.challenges.map((challenge) => challenge.challenge),
      );
      expect(closedList.challenges.every((challenge) => challenge.state === "closed")).toBeTruthy();

      await page.goto("/challenges");
      const finished = await page
        .locator('#challenges-index .challenge-group[data-state="closed"] tr.challenge-row')
        .evaluateAll((rows) => rows.map((row) => (row as HTMLElement).dataset.challenge));
      expect(finished).toEqual(closedList.challenges.map((challenge) => challenge.challenge));

      const archiveResponse = await page.request.get(`/v1/challenges/${openChallenge.challenge}`);
      expect(archiveResponse.ok()).toBeTruthy();
      const archive = (await archiveResponse.json()) as ChallengeDetail;
      expect(archive.state).toBe("closed");
      expect(archive.rows).toEqual(openDetail.rows);

      await page.goto(`/challenges/${openChallenge.challenge}`);
      await expect(page.locator("#challenge-metadata")).toHaveAttribute("data-state", "closed");
      await expect(page.locator("#challenge-deadline")).toHaveCount(0);
      const archiveValues = await page
        .locator("#challenge-standings tr.challenge-holder td.value")
        .evaluateAll((cells) => cells.map((cell) => Number((cell as HTMLElement).dataset.value)));
      expect(archiveValues).toEqual(archive.rows.map((row) => row.value));

      await page.goto("/");
      await expect(page.locator("#open-challenge")).toHaveCount(0);
    } finally {
      await admin(request, "/admin/clock", { at_ms: WEEK_33_OPEN });
    }
  });
});
