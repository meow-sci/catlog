/**
 * The wire shapes of catlog's public read API (INITIAL_IMPL_PLAN.md §4.8).
 *
 * Hand-written rather than generated: the API has four endpoints and they are
 * pinned by Go tests, so a generator would be more machinery than the thing it
 * generates. Every field here is one the server actually sends — `context` is
 * the only one that is board-specific, and it is left as `unknown` because
 * nothing in this app may assume a shape the server documents as free-form JSON.
 */

/** One entry of `GET /v1/leaderboards`. */
export interface BoardSummary {
  stat: string;
  title: string;
  /** Labels the value column. Never a conversion factor. */
  unit: string;
  /** How many players are on the board. Includes banned players; see §4.8. */
  count: number;
}

export interface BoardsResponse {
  boards: BoardSummary[];
}

/** One row of `GET /v1/leaderboards/{stat}`. */
export interface BoardRow {
  rank: number;
  handle: string;
  value: number;
  /** Board-specific detail (body, flight, energy_j …). Absent on counter boards. */
  context?: unknown;
  /** Server receive time of the event that set this value, unix ms. */
  updated: number;
}

export interface BoardResponse {
  stat: string;
  title: string;
  unit: string;
  /** The effective paging, after the server clamped it. */
  limit: number;
  offset: number;
  rows: BoardRow[];
}

/** One of a player's board placements. */
export interface PlayerStat {
  stat: string;
  title: string;
  unit: string;
  value: number;
  rank: number;
  context?: unknown;
  updated: number;
}

export interface PlayerResponse {
  handle: string;
  /** When the handle was claimed, unix ms. */
  since: number;
  stats: PlayerStat[];
}

/** One line of the activity feed. */
export interface FeedRow {
  id: number;
  /** Server receive time, unix ms. Never the client's wall clock (§4.1). */
  at: number;
  handle: string;
  type: string;
  summary: string;
}

export interface FeedResponse {
  limit: number;
  rows: FeedRow[];
}
