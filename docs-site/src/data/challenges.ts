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

/** No challenge definitions ship in H2; H4 supplies the first catalogue entries. */
export const CHALLENGES: Challenge[] = [];

export function challengeByKey(challenge: string): Challenge | undefined {
  return CHALLENGES.find((candidate) => candidate.challenge === challenge);
}
