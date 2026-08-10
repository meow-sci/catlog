/**
 * The machine-readable mirror of the board catalog.
 *
 * DERIVED DATA. `docs/event-details.md` in the repository root is the primary
 * reference and wins any disagreement. See AGENTS.md and CONSTITUTION.md §9.1.
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
  /**
   * The ranking dimensions this board supports. This is distinct from
   * `career`: that flag describes the value's clock, while scopes describe
   * whose rows are ranked.
   */
  scopes: ("player" | "career" | "system")[];
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

const UNIVERSAL_SCOPES: Board["scopes"] = ["player", "career", "system"];

/** The 43 fixed boards, in the order the site publishes them. */
const BOARD_DEFINITIONS: Omit<Board, "scopes">[] = [
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
    stat: "max_q_survived",
    title: "Max Q Survived",
    unit: "Pa",
    ascending: false,
    career: false,
    from: ["telemetry.window"],
    what: "The hardest the air ever pushed back on you — and you still came home.",
    how: "The largest dynamic-pressure figure across every 30-second telemetry window. Peak g is how hard the airframe was squeezed; max q is how hard the air was pushing, and an ascent profile can be brutal on one and gentle on the other.",
    excluded: [
      "The game did not compute a reading for that window — on rails or in freefall there is nothing to report, and catlog leaves it out rather than calling it zero.",
      "The flight was flagged.",
      "The flight did not end recovered (applied when the leaderboards are rebuilt).",
    ],
  },
  {
    stat: "biggest_impact_energy",
    title: "Biggest Bang Survived",
    unit: "J",
    ascending: false,
    career: false,
    from: ["vehicle.impact"],
    what: "The most violent arrival your vehicle absorbed and shrugged off.",
    how: "The largest impact energy the game reported for a crash you walked away from. Because it is energy rather than speed, it ranks a heavy lander coming down hard above a light probe coming in fast — the exact opposite of Biggest Lithobrake Survived, which is why both boards exist. Same crash, two honest readings.",
    excluded: [
      "The energy came out as zero.",
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
    stat: "fastest_entry",
    title: "Fastest Atmospheric Entry",
    unit: "m/s",
    ascending: false,
    career: false,
    from: ["vehicle.atmosphere"],
    what: "The fastest you have ever hit an atmosphere.",
    how: "Your speed relative to the surface at the moment you crossed into an atmosphere. Leaving one fast does not count — that is an ascent, and the speed boards already rank it. Entering one fast is the part that usually ends in a fireball.",
    excluded: ["The speed came out as zero.", "The flight was flagged."],
  },
  {
    stat: "highest_altitude",
    title: "Highest Altitude",
    unit: "m",
    ascending: false,
    career: false,
    from: ["telemetry.window"],
    what: "The highest anything of yours has ever been.",
    how: "The largest altitude across every 30-second telemetry window, measured from the average radius of the world you were at — so landing on a mountain scores the mountain, and skimming a canyon does not lose you anything. Unlike the g and pressure boards the flight does not have to come home: an altitude is a position, always readable, and a probe that never came back still got there.",
    excluded: ["The altitude was zero or below.", "The flight was flagged."],
  },
  {
    stat: "lowest_pass",
    title: "Lowest Pass",
    unit: "m",
    ascending: true,
    career: false,
    from: ["telemetry.window"],
    what: "The closest you came to the ground without ending up on it.",
    how: "The smallest height above whatever was directly underneath you, across every 30-second telemetry window. Lowest wins. This is deliberately the opposite altitude from Highest Altitude, which is measured from the world's average radius: a run down a canyon reads as high there and low here, and a hover over a mountaintop reads the other way round. A landing is not a pass — Softest Landing is the board for arriving.",
    excluded: [
      "The window carried no ground reading at all. In orbit there is nothing underneath to measure against, and catlog leaves the figure out entirely rather than calling it zero — the same rule peak g follows. Every window recorded before catlog started sending it is in the same position, so flights older than that score nothing here.",
      "A reading of zero or below. Zero is where a vehicle sitting on the ground reads, so every flight would tie on it before it ever left the pad, and on a board where the smallest number wins nobody could beat that.",
      "The flight was flagged.",
    ],
  },
  {
    stat: "highest_apoapsis",
    title: "Highest Apoapsis",
    unit: "m",
    ascending: false,
    career: false,
    from: ["vehicle.orbit"],
    what: "The far end of the widest orbit you have ever settled into.",
    how: "The greatest apoapsis altitude of any orbit you actually reached, measured above the world's average radius rather than from its centre. The same event records semi-major axis, node and periapsis directions, periapsis time and period, but no leaderboard scores those five figures.",
    excluded: [
      "The path was not a closed orbit. A fly-by or an escape has no far end to report, and catlog records nothing rather than a nonsense number.",
      "The flight was flagged.",
    ],
  },
  {
    stat: "lowest_orbit",
    title: "Lowest Stable Orbit",
    unit: "m",
    ascending: true,
    career: false,
    from: ["vehicle.orbit"],
    what: "The closest you have skimmed a world and stayed in orbit.",
    how: "The smallest periapsis altitude of any orbit you settled into. Lowest wins.",
    excluded: [
      "A low point at or below zero. An orbit whose near side is underground is not a lower orbit, it is a landing in progress — and on a board where the smallest number wins, it would be a record nobody could beat.",
      "The flight was flagged.",
    ],
  },
  {
    stat: "roundest_orbit",
    title: "Roundest Orbit",
    unit: "",
    ascending: true,
    career: false,
    from: ["vehicle.orbit"],
    what: "The most perfectly circular orbit you have flown.",
    how: "The smallest eccentricity of any orbit you settled into. Zero would be a perfect circle, so lower wins and the number carries no unit — it is a ratio, not a measurement of anything.",
    excluded: [
      "An eccentricity of exactly zero. No real orbit is exactly circular, so a flat zero means the figure was never read — and on a board where the smallest number wins it would take the record permanently.",
      "The flight was flagged.",
    ],
  },
  {
    stat: "steepest_orbit",
    title: "Most Inclined Orbit",
    unit: "deg",
    ascending: false,
    career: false,
    from: ["vehicle.orbit"],
    what: "The most tilted orbit you have flown.",
    how: "The largest inclination, in degrees, of any orbit you settled into. A polar orbit is around 90; going over the top and coming back around the other way is more than that.",
    excluded: [
      "An inclination of exactly zero, which means the figure was never read.",
      "The flight was flagged.",
    ],
  },
  {
    stat: "softest_touchdown",
    title: "Softest Touchdown",
    unit: "m/s",
    ascending: true,
    career: false,
    from: ["vehicle.situation"],
    what: "The gentlest landing you have ever put down.",
    how: "Your surface speed at the moment you went from being off the ground to touching it. Lowest wins.",
    excluded: [
      "You were already touching something. A rover rolling to a stop, or a lander sliding on a slope, is a surface-to-surface move at almost no speed — not a touchdown.",
      "catlog could not tell what you were doing beforehand. An unreadable state is not a measurement, and on a board where the smallest number wins it would take the record.",
      "The speed came out as zero.",
      "The flight was flagged.",
    ],
  },
  {
    stat: "softest_landing",
    title: "Softest Landing",
    unit: "m/s",
    ascending: true,
    career: false,
    from: ["vehicle.landed"],
    what: "Your gentlest touchdown, measured by descent rate alone.",
    how: "How fast you were coming down at the moment catlog saw you touch, and nothing else. Lowest wins. Softest Touchdown ranks the same moment by your whole speed relative to the ground: a rover arriving at 8 m/s across a plain and a lander arriving at 8 m/s straight down are the same number there and very different flying. This board is the vertical half on its own — the one you are actually managing on the way in.",
    excluded: [
      "The vehicle did not survive. That is a crash, and the impact boards are where a crash belongs.",
      "The descent rate came out as exactly zero, which is what an unreadable measurement leaves behind. A real touchdown is never exactly zero — catlog looks twice a second and the vehicle is still settling — and on a board where the smallest number wins, a zero would be unbeatable.",
      "The flight was flagged.",
    ],
  },
  {
    stat: "heaviest_launch",
    title: "Heaviest Launch",
    unit: "kg",
    ascending: false,
    career: false,
    from: ["flight.started"],
    what: "The biggest thing you have ever got off the ground.",
    how: "The total mass of the heaviest vehicle you have started a flight with.",
    excluded: [
      "The mass read as zero, which means it could not be read at all rather than that the vehicle weighed nothing.",
      "The flight was flagged.",
    ],
  },
  {
    stat: "heaviest_to_orbit",
    title: "Heaviest Payload To Orbit",
    unit: "kg",
    ascending: false,
    career: false,
    from: ["vehicle.orbit"],
    what: "The heaviest thing you have ever put into a stable orbit around anything.",
    how: "What the vehicle weighed at the instant it settled into orbit. The pairing with Heaviest Launch is the point of it: what left the pad includes the propellant that will be burned getting off it, and what is still there when the orbit milestone fires is the payload.",
    excluded: [
      "You escaped rather than orbited. An escape is not an orbit anybody reached.",
      "The mass came out as zero or below. That is also what every orbit recorded before catlog started sending this figure looks like, so flights older than that score nothing here — and a rebuild cannot rescue them, because the number was never written down.",
      "The flight was flagged.",
    ],
  },
  {
    stat: "most_parts",
    title: "Most Parts",
    unit: "",
    ascending: false,
    career: false,
    from: ["flight.started"],
    what: "Your most elaborate machine, counted in parts.",
    how: "The largest part count of any vehicle you have started a flight with. The number is shown on its own, because the title already says what is being counted.",
    excluded: ["The part count read as zero.", "The flight was flagged."],
  },
  {
    stat: "biggest_stack",
    title: "Most Stages Built",
    unit: "",
    ascending: false,
    career: false,
    from: ["flight.started"],
    what: "The tallest stack you have ever flown, counted in stages.",
    how: "How many stages the vehicle was built with, read as the flight starts. Most Stages is the highest stage you ever actually fired: a five-stage rocket that comes apart on stage two scores five here and two there. The number is shown on its own, because the title already says what is being counted.",
    excluded: [
      "The stage count read as zero. That is also what every flight recorded before catlog started sending this figure looks like, so flights older than that score nothing here, and a rebuild cannot rescue them.",
      "The flight was flagged.",
    ],
  },
  {
    stat: "biggest_crew",
    title: "Biggest Crew",
    unit: "kittens",
    ascending: false,
    career: false,
    from: ["flight.started"],
    what: "The most kittens you have ever sent up at once.",
    how: "The largest number of occupied seats on any vehicle at the moment it started flying. Occupied seats, not seats — an empty chair is not a crew member.",
    excluded: ["Nobody was aboard.", "The flight was flagged."],
  },
  {
    stat: "biggest_recovery",
    title: "Most Kittens Home At Once",
    unit: "kittens",
    ascending: false,
    career: false,
    from: ["flight.ended"],
    what: "The most kittens you have ever brought home on a single flight.",
    how: "The largest crew count on any one flight that ended recovered. Kittens Recovered adds every trip together; this is the best single trip. Forty solo recoveries and one nine-seat station return are the same number there and very different here.",
    excluded: [
      "The flight did not end recovered.",
      "Nobody was aboard.",
      "The flight was flagged.",
    ],
  },
  {
    stat: "most_stages",
    title: "Most Stages",
    unit: "",
    ascending: false,
    career: false,
    from: ["vehicle.staging"],
    what: "The most stages you have burned through on one vehicle.",
    how: "The highest stage any vehicle of yours ever reached. Firing your first stage counts as one, so a single-stage flight scores one rather than nothing. The number is shown on its own — the title already names what is counted.",
    excluded: ["The flight was flagged."],
  },
  {
    stat: "longest_eva",
    title: "Longest Spacewalk",
    unit: "s",
    ascending: false,
    career: false,
    from: ["kitten.eva_end"],
    what: "The longest single spacewalk one of your kittens has taken.",
    how: "The time from a kitten stepping outside to coming back in, in game seconds. Longest wins.",
    excluded: [
      "The duration came out as zero, which means the moment the spacewalk began could not be read.",
      "Nothing else. The end of a spacewalk is not attached to a flight, so the flag rule has nothing to attach to either.",
    ],
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
    how: "One per world, the first time you arrive at it. Your player total is the union across every save; a career row counts only that save; a system row is the union across your saves using that celestial system. Arriving again within the same scope changes nothing.",
    excluded: ["The flight was flagged."],
  },
  {
    stat: "landed_bodies",
    title: "Bodies Landed On",
    unit: "bodies",
    ascending: false,
    career: false,
    from: ["vehicle.situation"],
    what: "How many different worlds you have put something down on.",
    how: "One per world, the first time you touch its surface. Your player total is the union across every save; a career row counts only that save; a system row is the union across your saves using that celestial system. Water counts: splashing down on a world is still arriving at it, and Splashdowns is the board that tells the two apart. It asks whether you have anything standing on a surface there, which is a wider question than Landings: a vehicle already sitting on the ground when a save loads counts here, and so does a rover that rolls to a stop, and neither of those is a landing.",
    excluded: ["The world's name could not be read.", "The flight was flagged."],
  },
  {
    stat: "landings",
    title: "Landings",
    unit: "landings",
    ascending: false,
    career: false,
    from: ["vehicle.landed"],
    what: "How many times you have put something down and had it survive.",
    how: "One per landing, at any speed — a landing is a landing. Bodies Landed On counts the worlds instead, and asks the wider question: whether you have anything on a surface there at all. The two count different things, and a touchdown never reaches both through the same door.",
    excluded: [
      "The vehicle did not survive. That is a crash, not a landing.",
      "The flight was flagged.",
    ],
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
    stat: "splashdowns",
    title: "Splashdowns",
    unit: "splashdowns",
    ascending: false,
    career: false,
    from: ["vehicle.situation"],
    what: "How many times you have come down in water.",
    how: "One per arrival in water: you have to have been off the ground beforehand, and floating or under sail afterwards.",
    excluded: [
      "You were already touching a surface. That is what stops a boat drifting on and off the physics simulation from scoring a splashdown every few seconds.",
      "You ended up dragging on the bottom or grounded in the shallows. Those touch land as well, and are a hull on a shoreline rather than a capsule under a parachute.",
      "The flight was flagged.",
    ],
  },
  {
    stat: "evas",
    title: "Spacewalks",
    unit: "EVAs",
    ascending: false,
    career: false,
    from: ["kitten.eva_start"],
    what: "How many times a kitten of yours has gone outside.",
    how: "One per spacewalk started.",
    excluded: [
      "The flight was flagged — but only when catlog could tell which vehicle the kitten stepped out of. When it could not, there is no flight for the rule to check.",
    ],
  },
  {
    stat: "flameouts",
    title: "Ran Dry",
    unit: "flameouts",
    ascending: false,
    career: false,
    from: ["engine.flameout"],
    what: "How many times you have run an engine dry in the middle of a burn.",
    how: "One each time your engines are running and the propellant stops arriving. The game has no idea of a flameout, so catlog watches for the combination itself: still burning, nothing left to burn.",
    excluded: ["The flight was flagged."],
  },
  {
    stat: "engine_ignitions",
    title: "Engines Lit",
    unit: "ignitions",
    ascending: false,
    career: false,
    from: ["engine.ignition"],
    what: "How many times you have lit the engines.",
    how: "One each time a vehicle goes from nothing running to something running. Shutting down is recorded and scores nothing — it is the other half of every burn, so counting it would be counting ignitions twice.",
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
    what: "Total distance travelled by your whole crew across your saves.",
    how: "Each save keeps its own roster totals. Your player total adds every save; a career row adds that save; a system row adds every save using that celestial system. Kittens with the same roster name in different saves still count separately, because they are different cats. Within one save, an older snapshot can fail to move a total but can never wind it back.",
    excluded: [
      "Nothing. Roster totals are not attached to a flight, so the flag rule has nothing to attach to either. The two per-kitten boards below inherit the same gap.",
    ],
  },
  {
    stat: "top_kitten_distance",
    title: "Furthest-Travelled Kitten",
    unit: "m",
    ascending: false,
    career: false,
    from: ["roster.snapshot"],
    what: "How far your single most-travelled kitten has gone.",
    how: "The largest distance held by any one kitten in the selected player, career or celestial-system scope. Distance Travelled adds the whole roster in that scope together; this one is the best individual cat, and the row names her.",
    excluded: [
      "Nothing. Roster totals are not attached to a flight, so the flag rule has nothing to attach to either — a kitten who did all her travelling on a flagged flight still holds the record.",
    ],
  },
  {
    stat: "top_kitten_missions",
    title: "Most Missions Flown",
    unit: "missions",
    ascending: false,
    career: false,
    from: ["roster.snapshot"],
    what: "How many missions your most-flown kitten has on record.",
    how: "The largest mission count held by any one kitten in the selected player, career or celestial-system scope. The game counts a mission that was called off before launch, so this rewards showing up as well as arriving.",
    excluded: [
      "Nothing, for the same reason as the board above: roster totals carry no flight to flag.",
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
  {
    stat: "career_playtime",
    title: "Longest Save",
    unit: "ms",
    ascending: false,
    career: true,
    from: ["any event carrying career + sim_t"],
    what: "How long your longest-running save has been played.",
    how: "The largest positive career-clock reading carried by an event in the selected scope. A career row is the time played in that save; the player row is your longest-running save; a celestial-system row is the longest-running save using that system.",
    excluded: [
      "An event with no save or no career-clock reading, or a clock reading of zero or less.",
      "Nothing based on a flight flag. A duration is not a feat: a flagged flight is still time spent playing that save.",
    ],
  },
  {
    stat: "play_sessions",
    title: "Play Sessions",
    unit: "sessions",
    ascending: false,
    career: false,
    from: ["session.started"],
    what: "How many times you have started or loaded the game into a save.",
    how: "One for the initial session and one for every later load in the selected player, save or celestial-system scope. This counts play sessions, not just resumes.",
    excluded: ["Nothing."],
  },
  {
    stat: "botched_landings",
    title: "Did Not Land On Their Feet",
    unit: "tumbles",
    ascending: false,
    career: false,
    from: ["kitten.tumble"],
    what: "How many tumbles began as a landing that did not end on the kitten's feet.",
    how: "One for each tumble whose immediately previous movement state was airborne. Grounded trips and every other previous state still count on Kitten Tumbles, but not here.",
    excluded: [
      "The tumble began from grounded, unknown, or any state other than airborne.",
      "The flight was flagged, which includes any flight where the tumble tuning was changed.",
    ],
  },
];

// Scope is deliberately attached here rather than repeated on 43 entries. A
// board added to the catalog receives every scope automatically; there is no
// opt-out registry to forget to update.
export const BOARDS: Board[] = BOARD_DEFINITIONS.map((board) => ({
  ...board,
  scopes: [...UNIVERSAL_SCOPES],
}));

/** Boards whose keys are built from the data rather than fixed in advance. */
const BOARD_FAMILY_DEFINITIONS: Omit<Board, "scopes">[] = [
  {
    stat: "tumbles_on_<body>",
    title: "Tumbles on <Body>",
    unit: "tumbles",
    ascending: false,
    career: false,
    from: ["kitten.tumble"],
    what: "How many times your kittens have gone over on one particular world.",
    how: "One per tumble on that world, in addition to the all-world Kitten Tumbles total.",
    excluded: [
      "The flight was flagged.",
      "The world's name could not safely form a board key. The tumble still counts towards Kitten Tumbles.",
    ],
    family: {
      pattern: "tumbles_on_<body>",
      from: "The name of the world where the tumble happened. Catlog has no list of allowed worlds; a family member appears because somebody tumbled there.",
    },
  },
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

export const BOARD_FAMILIES: Board[] = BOARD_FAMILY_DEFINITIONS.map((board) => ({
  ...board,
  scopes: [...UNIVERSAL_SCOPES],
}));

export function boardByStat(stat: string): Board | undefined {
  return [...BOARDS, ...BOARD_FAMILIES].find((b) => b.stat === stat);
}
