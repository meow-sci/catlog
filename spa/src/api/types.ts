/**
 * The wire shapes of catlog's public read API (INITIAL_IMPL_PLAN.md §4.8).
 *
 * Hand-written rather than generated: the API has four endpoints and they are
 * pinned by Go tests, so a generator would be more machinery than the thing it
 * generates. The source of truth is `server/internal/readapi/readapi.go` — every
 * field below is mirrored from a JSON tag there, and a field the server sends
 * that is missing here is a bug, not an omission: the SPA cannot render what it
 * does not know about, and `ascending` going missing is how a "fastest to orbit"
 * board comes to be presented as though slower were better.
 *
 * `context` is the one field left as `unknown`, because nothing in this app may
 * assume a shape the server documents as free-form JSON.
 */

/** One entry of `GET /v1/leaderboards`. */
export interface BoardSummary {
  stat: string;
  title: string;
  /** Labels the value column. Never a conversion factor. */
  unit: string;
  /**
   * The *smallest* value ranks first.
   *
   * True on the career-time boards (`fastest_to_orbit`, `fastest_to_<body>`),
   * where the value is seconds since the career began. Published so a client
   * never has to guess which way a board reads.
   */
  ascending: boolean;
  /** How many players are on the board. Includes banned players; see §4.8. */
  count: number;
}

export interface BoardsResponse {
  boards: BoardSummary[];
  /**
   * How many distinct players a board whose key came out of the event stream
   * (`fastest_to_<body>`, `rud_<cause>`) needs before it is listed.
   *
   * catlog keeps no list of celestial bodies — they are game content, opaque to
   * the server — so those boards appear because players went there. This number
   * is why one may not have appeared yet.
   */
  min_players: number;
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
  /**
   * Set on a career-time value whose career has had an earlier save loaded.
   *
   * It qualifies the number and does nothing else: the row is ranked normally
   * and the player is treated no differently (§4.1, docs/events.md).
   */
  rewound?: boolean;
}

export interface BoardResponse {
  stat: string;
  title: string;
  unit: string;
  /** The smallest value ranks first; see [BoardSummary.ascending]. */
  ascending: boolean;
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
  /** Qualifies a career-time value; see [BoardRow.rewound]. */
  rewound?: boolean;
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
