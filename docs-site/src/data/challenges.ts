/**
 * The machine-readable mirror of the weekly challenge catalogue.
 *
 * DERIVED DATA. `docs/event-details.md` in the repository root is the primary
 * reference and wins any disagreement. See AGENTS.md and docs/CONSTITUTION.md §9.1.
 */

export type ChallengeScope = "player" | "career" | "system";

export interface Challenge {
  /** The stable challenge key used by the projection. */
  challenge: string;
  title: string;
  /** The rule in the player's words. */
  blurb: string;
  /** Inclusive opening instant, as Unix milliseconds in UTC. */
  opens: number;
  /** Exclusive closing instant, as Unix milliseconds in UTC. */
  closes: number;
  unit: string;
  /** True when the smallest value wins. */
  ascending: boolean;
  /** Whether results are kept per player, per save or per player and system. */
  scope: ChallengeScope;
}

const WEEK_33_OPENS = 1_786_320_000_000; // 2026-08-10T00:00:00Z, inclusive.
const WEEK_33_CLOSES = 1_786_924_800_000; // 2026-08-17T00:00:00Z, exclusive.

/** The six Week 33 starter challenges, in display order. */
export const CHALLENGES: Challenge[] = [
  {
    challenge: "heavy_lift_week",
    title: "Heavy Lift Week",
    blurb:
      "Get the heaviest payload you can into orbit. The number is what the whole vehicle weighed the moment it got there, propellant included — catlog cannot tell the cargo from the rocket, and does not try.",
    opens: WEEK_33_OPENS,
    closes: WEEK_33_CLOSES,
    unit: "kg",
    ascending: false,
    scope: "system",
  },
  {
    challenge: "speedrun_orbit",
    title: "From Scratch To Orbit",
    blurb:
      "Start a save and get to orbit. The clock is the game clock, counted from the beginning of that save.",
    opens: WEEK_33_OPENS,
    closes: WEEK_33_CLOSES,
    unit: "ms",
    ascending: true,
    scope: "career",
  },
  {
    challenge: "tumbleweek",
    title: "Tumbleweek",
    blurb: "The most kitten tumbles",
    opens: WEEK_33_OPENS,
    closes: WEEK_33_CLOSES,
    unit: "tumbles",
    ascending: false,
    scope: "player",
  },
  {
    challenge: "coasting_class",
    title: "Coasting Class",
    blurb:
      "The most distinct worlds reached in-window on flights that launched with no engine installed. RCS thrusters and other non-engine propulsion still qualify.",
    opens: WEEK_33_OPENS,
    closes: WEEK_33_CLOSES,
    unit: "bodies",
    ascending: false,
    scope: "system",
  },
  {
    challenge: "feather_touch",
    title: "Feather Touch",
    blurb: "The gentlest surviving landing away from that system's home body",
    opens: WEEK_33_OPENS,
    closes: WEEK_33_CLOSES,
    unit: "m/s",
    ascending: true,
    scope: "system",
  },
  {
    challenge: "full_house",
    title: "Full House",
    blurb: "The most kittens brought home in one piece at once",
    opens: WEEK_33_OPENS,
    closes: WEEK_33_CLOSES,
    unit: "kittens",
    ascending: false,
    scope: "player",
  },
];

export function challengeByKey(challenge: string): Challenge | undefined {
  return CHALLENGES.find((candidate) => candidate.challenge === challenge);
}
