/**
 * The machine-readable mirror of the board catalog.
 *
 * DERIVED DATA. `docs/event-details.md` in the repository root is the primary
 * reference and wins any disagreement. See CLAUDE.md and CONSTITUTION.md §9.1.
 */

export interface Board {
  /** The board key, as it appears in the read API and in URLs. */
  stat: string;
  title: string;
  /**
   * The unit label. Note `ms` on a board means MILLISECONDS, while a data key
   * ending `_ms` means metres per second. They are genuinely different things.
   */
  unit: string;
  /** True when the smallest value wins. */
  ascending: boolean;
  /** True when the value is a time measured from the start of a career. */
  career: boolean;
  /** Event types that move this board. */
  from: string[];
  /** What the number is, in one line. */
  what: string;
  /** How the number is chosen from the events. */
  how: string;
  /** Everything that disqualifies a candidate value. */
  excluded: string[];
  /** Set when the key is generated from event data rather than fixed. */
  family?: { pattern: string; from: string };
}

/** The 13 fixed boards, in the order the site publishes them. */
export const BOARDS: Board[] = [
  {
    stat: "biggest_lithobrake_survived",
    title: "Biggest Lithobrake Survived",
    unit: "m/s",
    ascending: false,
    career: false,
    from: ["vehicle.impact"],
    what: "The hardest landing you walked away from.",
    how: "The single largest impact speed on any impact where the vehicle still existed a frame later. Kept as-is in metres per second — no rounding, no scaling.",
    excluded: [
      "The vehicle was destroyed in the same frame or the next one.",
      "You destroyed it yourself right afterwards.",
      "You hit the launch pad.",
      "Nobody was aboard.",
      "The impact was within 5 seconds of a teleport.",
      "The flight was flagged.",
      "A kitten was lost within 2 seconds of the impact (applied when the leaderboards are rebuilt).",
    ],
  },
  {
    stat: "peak_g_survived",
    title: "Peak G Survived",
    unit: "g",
    ascending: false,
    career: false,
    from: ["telemetry.window"],
    what: "The highest g-load your vehicle came through.",
    how: "The largest peak-g figure across every 30-second telemetry window.",
    excluded: [
      "The game did not compute a g-load for that window — on rails or in freefall there is nothing to report, and catlog leaves it out rather than calling it zero.",
      "The flight was flagged.",
      "The flight did not end recovered (applied when the leaderboards are rebuilt).",
    ],
  },
  {
    stat: "fastest_surface_speed",
    title: "Fastest Surface Speed",
    unit: "m/s",
    ascending: false,
    career: false,
    from: ["telemetry.window"],
    what: "Your highest speed relative to the surface below you.",
    how: "The largest surface-speed maximum across every 30-second telemetry window.",
    excluded: ["The flight was flagged."],
  },
  {
    stat: "fastest_orbital_speed",
    title: "Fastest Orbital Speed",
    unit: "m/s",
    ascending: false,
    career: false,
    from: ["telemetry.window"],
    what: "Your highest speed relative to the body you are orbiting.",
    how: "The largest orbital-speed maximum across every 30-second telemetry window.",
    excluded: ["The flight was flagged."],
  },
  {
    stat: "kitten_tumbles",
    title: "Kitten Tumbles",
    unit: "tumbles",
    ascending: false,
    career: false,
    from: ["kitten.tumble"],
    what: "How many times your kittens have gone over.",
    how: "One per tumble. Nothing about the tumble is read — the event itself is the whole signal.",
    excluded: [
      "The flight was flagged, which includes any flight where the tumble tuning was changed.",
    ],
  },
  {
    stat: "rud_total",
    title: "Rapid Unscheduled Disassemblies",
    unit: "RUDs",
    ascending: false,
    career: false,
    from: ["vehicle.rud"],
    what: "How many vehicles you have lost, all causes.",
    how: "One per destruction.",
    excluded: ["The flight was flagged."],
  },
  {
    stat: "orbits_achieved",
    title: "Orbits Achieved",
    unit: "orbits",
    ascending: false,
    career: false,
    from: ["vehicle.orbit"],
    what: "How many times you have reached a stable orbit.",
    how: "One per orbit achieved. Escaping an orbit counts for nothing.",
    excluded: ["The flight was flagged."],
  },
  {
    stat: "soi_bodies",
    title: "Bodies Visited",
    unit: "bodies",
    ascending: false,
    career: false,
    from: ["vehicle.soi"],
    what: "How many distinct worlds you have reached.",
    how: "One per world, the first time you arrive at it. Arriving again changes nothing.",
    excluded: ["The flight was flagged."],
  },
  {
    stat: "dockings",
    title: "Dockings",
    unit: "dockings",
    ascending: false,
    career: false,
    from: ["vehicle.docked"],
    what: "How many dockings you have completed.",
    how: "One per dock. Undocking is recorded but scores nothing.",
    excluded: ["The flight was flagged."],
  },
  {
    stat: "stagings",
    title: "Stagings",
    unit: "stagings",
    ascending: false,
    career: false,
    from: ["vehicle.staging"],
    what: "How many times you have staged.",
    how: "One per stage activation.",
    excluded: ["The flight was flagged."],
  },
  {
    stat: "kittens_recovered",
    title: "Kittens Recovered",
    unit: "kittens",
    ascending: false,
    career: false,
    from: ["flight.ended"],
    what: "How many kittens you have brought home.",
    how: "The crew count is added, not one — recovering a four-kitten vehicle adds four.",
    excluded: [
      "The flight did not end recovered.",
      "Nobody was aboard.",
      "The flight was flagged.",
    ],
  },
  {
    stat: "distance_travelled",
    title: "Distance Travelled",
    unit: "m",
    ascending: false,
    career: false,
    from: ["roster.snapshot"],
    what: "Total distance travelled across every kitten in your roster.",
    how: "Each kitten's running total is kept at its highest value ever seen, and the board is the sum of those. A snapshot that arrives out of order, or an older save reloaded, can fail to move the number but can never wind it back.",
    excluded: [
      "Nothing. This is the one board with no flight to flag — roster totals are not attached to a flight, so the flag rule has nothing to attach to either.",
    ],
  },
  {
    stat: "fastest_to_orbit",
    title: "Fastest to Orbit",
    unit: "ms",
    ascending: true,
    career: true,
    from: ["vehicle.orbit"],
    what: "How quickly, from the start of a career, you first reached orbit.",
    how: "The smallest career time at which any of your unflagged flights achieved orbit. Lowest wins.",
    excluded: [
      "The flight was flagged.",
      "The event carried no career or no clock reading — an absent time is not treated as zero.",
    ],
  },
];

/** Boards whose keys are built from the data rather than fixed in advance. */
export const BOARD_FAMILIES: Board[] = [
  {
    stat: "rud_<cause>",
    title: "RUDs — <Cause>",
    unit: "RUDs",
    ascending: false,
    career: false,
    from: ["vehicle.rud"],
    what: "How many vehicles you have lost to one particular cause.",
    how: "One per destruction with that cause, on top of the total board.",
    excluded: ["The flight was flagged."],
    family: {
      pattern: "rud_<cause>",
      from: "The cause the game blamed. Six exist today; a cause a future game build introduces gets its own board automatically, with no change to catlog.",
    },
  },
  {
    stat: "fastest_to_<body>",
    title: "Fastest to <Body>",
    unit: "ms",
    ascending: true,
    career: true,
    from: ["vehicle.soi"],
    what: "How quickly, from the start of a career, you first reached one particular world.",
    how: "The smallest career time at which any of your unflagged flights arrived at that world. Lowest wins.",
    excluded: ["The flight was flagged.", "The event carried no career or no clock reading."],
    family: {
      pattern: "fastest_to_<body>",
      from: "The name of the world you arrived at. There is no list of allowed worlds anywhere in catlog — a board exists because somebody went there.",
    },
  },
];

export function boardByStat(stat: string): Board | undefined {
  return [...BOARDS, ...BOARD_FAMILIES].find((b) => b.stat === stat);
}
