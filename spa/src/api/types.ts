/**
 * The wire shapes of catlog's public read API (INITIAL_IMPL_PLAN.md §4.8).
 *
 * Hand-written rather than generated: the endpoints are few and they are pinned
 * by Go tests, so a generator would be more machinery than the thing it
 * generates. **The source of truth is `server/internal/readapi/`** — every field
 * below is mirrored from a JSON tag in `readapi.go`, `search.go`, `compare.go`,
 * `events.go` or `feed.go`, and a field the server sends that is missing here is
 * a bug rather than an omission: the SPA cannot render what it does not know
 * about, and `ascending` going missing is how a "fastest to orbit" board comes
 * to be presented as though slower were better.
 *
 * `context` and `payload` are the two fields left as `unknown`, because nothing
 * in this app may assume a shape the server documents as free-form JSON.
 *
 * **What is not here, and never will be.** `user_key` appears in no response
 * struct in that package and in no type here; `wall_t` — the untrusted client
 * clock — is deliberately omitted from `EventRow` server-side; and `install` is
 * dropped rather than published, with `career` and `kid` relabelled per player
 * (`readapi/privacy.go`). A type that reintroduced any of them would be a
 * privacy bug even if nothing rendered it.
 */

// --- GET /v1/leaderboards ----------------------------------------------------

/** One entry of `GET /v1/leaderboards`. */
export interface BoardSummary {
  stat: string;
  title: string;
  /** Labels the value column. Never a conversion factor — see `ui/units.ts`. */
  unit: string;
  /**
   * The *smallest* value ranks first.
   *
   * True on the career-time boards (`fastest_to_orbit`, `fastest_to_<body>`),
   * where the value is seconds since the career began. Published so a client
   * never has to guess which way a board reads.
   */
  ascending: boolean;
  /** How many players are on the board. Counts rows, banned players included. */
  count: number;
  /**
   * The windows `?period=` accepts on this board.
   *
   * A period is a dimension of a board, not a board: the index stays one row per
   * board and says which windows that board can be read over.
   */
  periods: string[];
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

// --- GET /v1/leaderboards/{stat} ---------------------------------------------

/** One row of `GET /v1/leaderboards/{stat}`. */
export interface BoardRow {
  rank: number;
  handle: string;
  value: number;
  /** Board-specific detail (body, from, energy_j …). Absent on counter boards. */
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
  /** The window these rows cover — `alltime` unless `?period=` asked otherwise. */
  period: string;
  /**
   * The window's name — `2026-08-07`, `2026-W32`, `2026-08`, `2026`. Absent for
   * `alltime`. When `?at=` was not given this is the window the server's clock is
   * currently in, so a client can say which week it is looking at without
   * computing one.
   */
  bucket?: string;
  /** The effective paging, after the server clamped it. */
  limit: number;
  offset: number;
  rows: BoardRow[];
}

// --- GET /v1/players/{handle} ------------------------------------------------

/** One of a player's board placements. */
export interface PlayerStat {
  stat: string;
  title: string;
  unit: string;
  value: number;
  /**
   * The smallest value ranks first.
   *
   * Repeated here rather than looked up in the board index, because a profile
   * shows a rank next to a value and "#1 with the lowest number" is unreadable
   * without it.
   */
  ascending: boolean;
  /** The player's position among *visible* players on that board. */
  rank: number;
  /**
   * How many players hold a value on the board — the denominator that turns
   * `#3` into `#3 of 41`.
   *
   * It counts rows, **banned players included**, exactly like
   * [BoardSummary.count]. Rank is ban-filtered and this is not, so a rank can be
   * better than this number implies, never worse: a percentile computed from the
   * two must be clamped, never allowed past 100 %.
   */
  players: number;
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

// --- GET /v1/players?q= ------------------------------------------------------

/**
 * `GET /v1/players?q=` — handle search.
 *
 * Note the route: `/v1/players`, not `/v1/handles`.
 */
export interface SearchResponse {
  /** The effective query — trimmed, in the caller's casing. */
  query: string;
  /** The effective cap after clamping. */
  limit: number;
  /**
   * The matches: **prefix matches first, then substring matches**, each group in
   * lexicographic order of the lowercase handle, display casing preserved. So an
   * empty result says "no handles match", not "no handles start with".
   *
   * Banned and retired handles are absent by construction — the endpoint scans
   * the same in-memory directory every board page resolves through, and that map
   * already excludes them. There is nothing here to filter.
   */
  handles: string[];
  /**
   * More handles matched than `limit` allowed.
   *
   * It means **narrow the query**, not "load more": there is deliberately no
   * offset, because a paged search over a live directory is a promise the server
   * cannot keep.
   */
  truncated?: boolean;
}

// --- GET /v1/compare?handles= ------------------------------------------------

/** One column header of a comparison. */
export interface ComparePlayer {
  /** Display casing when the player exists; otherwise the string as asked for. */
  handle: string;
  /**
   * False for an unknown, retired **or** banned handle — one answer for all
   * three, on purpose. It is a column, not an omission: silently dropping it
   * would let a typo look like a defeat.
   */
  found: boolean;
  /** When the handle was claimed, unix ms. Absent when `found` is false. */
  since?: number;
}

/** One player's placement on one compared board. */
export interface CompareRow {
  handle: string;
  value: number;
  /**
   * The position among visible players on the **whole board**, not among the
   * compared handles: "3rd in the world", not "2nd of your friends".
   */
  rank: number;
  context?: unknown;
  updated: number;
  rewound?: boolean;
}

/** One row of a comparison: a board, and whichever compared players are on it. */
export interface CompareBoard {
  stat: string;
  title: string;
  unit: string;
  ascending: boolean;
  /** The banned-inclusive row count; see [PlayerStat.players]. */
  players: number;
  /**
   * The compared players who are on this board. A handle missing from here is
   * **not on the board** — absent, not zero, the same rule the folds follow for
   * a missing `peak_g`.
   */
  rows: CompareRow[];
}

export interface CompareResponse {
  /** The requested handles, deduplicated and capped, in column order. */
  handles: ComparePlayer[];
  /** Every board at least one of them is on, in board-index display order. */
  boards: CompareBoard[];
}

// --- GET /v1/players/{handle}/events -----------------------------------------

/** One stored event envelope, as the public API publishes it. */
export interface EventRow {
  /** The envelope's client-minted ULID — the dedup key. */
  id: string;
  type: string;
  ver: number;
  /**
   * The save-load boundary this event belongs to, and the flight.
   *
   * Both are per-occurrence ULIDs with nothing derived from the install in them.
   * `flight` is absent on session and roster events.
   */
  session?: string;
  flight?: string;
  /**
   * The career key, **relabelled per player** (`readapi/privacy.go`).
   *
   * It still groups this player's events by save, and it can no longer be
   * compared against another player's — which is the whole point: the raw value
   * is derived from the mod's install id, so publishing it would link two
   * handles belonging to one person.
   */
  career?: string;
  /** Seconds since this career's game started. Absent, not zero, when the event carried none. */
  sim_t?: number;
  /**
   * The **server's** receive time, unix ms.
   *
   * The client's own `wall_t` is not published: it is the untrusted clock, and
   * its offset from `recv` is a per-machine constant.
   */
  recv: number;
  /** The payload with the redaction applied, and otherwise verbatim — unknown keys included. */
  payload: unknown;
}

export interface EventsResponse {
  handle: string;
  /** The effective page size after clamping. */
  limit: number;
  /** Echoed when `?type=` was given. */
  type?: string;
  /**
   * The cursor for the next (older) page, **absent once the log is exhausted**.
   * Opaque: it is the value to pass back as `?before=` and nothing else.
   *
   * A short page carrying a cursor is *not* the end of the log — a filtered page
   * that hit the server's scan bound looks exactly like that — so a client pages
   * until this is absent, never until a page comes back short.
   */
  next?: string;
  /** Newest first. */
  events: EventRow[];
}

// --- GET /v1/feed ------------------------------------------------------------

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
