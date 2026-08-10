/**
 * The machine-readable mirror of the event catalog.
 *
 * This is DERIVED DATA. `docs/event-details.md` in the repository root is the
 * primary reference and wins any disagreement; when they differ, this file is
 * the thing to fix. See AGENTS.md and docs/CONSTITUTION.md §9.1.
 *
 * The prose here is player-facing: it says where a number comes from in terms a
 * player can check against their own screen. Code identifiers, file paths and
 * patch points belong in event-details.md and nowhere on this site.
 */

/** How the event is produced. */
export type Trigger =
  /** A discrete thing happened in the game and catlog was told about it directly. */
  | "event"
  /** Catlog samples the game twice a second and notices a change. */
  | "passive";

export type Family =
  | "system"
  | "session"
  | "flight"
  | "vehicle"
  | "engine"
  | "kitten"
  | "roster"
  | "telemetry";

export interface EventField {
  /** The exact key as it appears in the recorded data. */
  key: string;
  /** Unit, or "" when the value is a name, a flag or a plain count. */
  unit: string;
  /** What it is, in player terms. */
  what: string;
  /** True when the key can be absent entirely (not zero — absent). */
  optional?: boolean;
}

export interface CatlogEvent {
  /** The unique identifier. On the wire this is the `type` field. */
  type: string;
  family: Family;
  /** Contract version. Bumped whenever the shape of this event changes. */
  ver: number;
  /** One line: what this event means. */
  summary: string;
  trigger: Trigger;
  /** What in the game causes it — player language, no code. */
  cause: string;
  /** Where the numbers are read from — player language, no code. */
  source: string;
  /** Thresholds, debounces and hysteresis that decide whether it fires. */
  gate?: string;
  fields: EventField[];
  /** Board keys this event moves. Empty means it is recorded but scores nothing. */
  feeds: string[];
  /** True when the event is dropped first if the local spool fills up. */
  droppable?: boolean;
  /**
   * True when this event cannot be switched off in the mod's settings file.
   * Everything without it can be. The six that carry it are the integrity and
   * attribution spine: without one, a run can score as something it was not or
   * lose the celestial-system scope that gives its body names meaning.
   */
  alwaysOn?: boolean;
  /** Anchor on the family page. */
  page: string;
}

export const EVENTS: CatlogEvent[] = [
  {
    type: "system.discovered",
    alwaysOn: true,
    family: "system",
    ver: 1,
    summary: "This save is in a particular celestial system.",
    trigger: "event",
    cause:
      "When the game starts or you load a save, catlog identifies the celestial system before it starts the new play session.",
    source:
      "The system selected by the game, its launcher name and home world, plus the number of worlds that actually loaded. If the launcher has no matching name, catlog uses the system's raw id.",
    gate: "Always one header per real session. `complete` is false when no body list accompanies this header: the catalogue is switched off, too large, unreadable, or was already sent safely on an earlier load. A missing system produces nothing rather than a made-up identity.",
    fields: [
      {
        key: "system",
        unit: "",
        what: "A stable hash of the complete celestial catalogue. The same content gets the same hash for every player.",
      },
      { key: "id", unit: "", what: "The system's raw id." },
      {
        key: "name",
        unit: "",
        what: "The name shown in the game's system chooser, trimmed to 64 printable characters.",
      },
      { key: "home", unit: "", what: "The home world for this system." },
      { key: "bodies", unit: "", what: "How many worlds actually loaded, excluding vehicles." },
      {
        key: "complete",
        unit: "",
        what: "Whether every one of those worlds accompanies this header. False means no body list in this load — including after it was already sent safely once — never an empty system.",
      },
    ],
    feeds: [],
    page: "systems",
  },
  {
    type: "system.body",
    family: "system",
    ver: 1,
    summary: "One world in the celestial system's catalogue.",
    trigger: "event",
    cause:
      "The first time catlog sees a complete celestial system for this save, it writes one row for every world. It normally sends that catalogue only once for the save and system, even across restarts.",
    source:
      "The solar system your save loaded: each world's size, mass, atmosphere, ocean, spin, parent and orbit. These are the fixed properties the game uses to build the system, not a sample of where the worlds happen to be now.",
    gate: "All-or-nothing for the catalogue. More than 5,000 worlds, a required unreadable number, or switching this event off produces no body rows and leaves the header incomplete. Turning it back on lets a later load retry.",
    fields: [
      { key: "system", unit: "", what: "The stable catalogue hash shared with the system header." },
      {
        key: "body",
        unit: "",
        what: "The world's canonical join name — the same name flight events use.",
      },
      { key: "name", unit: "", what: "The world's raw in-game id, cleaned for display." },
      {
        key: "class",
        unit: "",
        what: "The game's own kind of object. This is open-ended so modded and future classes remain honest.",
      },
      {
        key: "kind",
        unit: "",
        what: "A broad `star`, `planet`, `moon`, `minor`, or `other` grouping.",
      },
      { key: "rank", unit: "", what: "How many parent steps the world is from its own root." },
      {
        key: "parent",
        unit: "",
        optional: true,
        what: "The world it orbits directly. Left out for a root.",
      },
      { key: "radius_m", unit: "m", what: "Mean radius from the world's centre." },
      { key: "mass_kg", unit: "kg", what: "Mass." },
      {
        key: "soi_m",
        unit: "m",
        what: "Sphere-of-influence radius. A root star uses zero because its in-game value is infinite.",
      },
      { key: "atmo_m", unit: "m", what: "Atmosphere height, zero when there is none." },
      {
        key: "ocean_m",
        unit: "m",
        what: "Ocean level above mean radius, zero when there is none.",
      },
      { key: "angvel", unit: "rad/s", what: "Signed spin rate; negative means retrograde." },
      { key: "axis", unit: "", what: "The three-dimensional spin axis." },
      {
        key: "ccf_to_cce_t0",
        unit: "",
        what: "The complete orientation at the start of the save's clock, including the prime-meridian phase.",
      },
      {
        key: "sma_m",
        unit: "m",
        optional: true,
        what: "Semi-major axis. This and the next five orbit-shape fields are all present together or all absent.",
      },
      { key: "ecc", unit: "", optional: true, what: "Orbital eccentricity." },
      { key: "inc_deg", unit: "°", optional: true, what: "Orbital inclination." },
      { key: "lan_deg", unit: "°", optional: true, what: "Longitude of ascending node." },
      { key: "argp_deg", unit: "°", optional: true, what: "Argument of periapsis." },
      {
        key: "t_pe",
        unit: "s",
        optional: true,
        what: "Absolute periapsis time on the save's own clock.",
      },
      {
        key: "period_s",
        unit: "s",
        optional: true,
        what: "Orbital period, left out independently for an unbound orbit.",
      },
    ],
    feeds: [],
    page: "systems",
  },
  {
    type: "session.started",
    alwaysOn: true,
    family: "session",
    ver: 1,
    summary: "You started playing, or loaded a save.",
    trigger: "event",
    cause:
      "The mod starts up with the game, and starts a fresh session again every time you load a save or begin a new game.",
    source:
      "The mod's own version, the game build string shown in the game's version info, and a random identifier generated once for this installation.",
    fields: [
      { key: "mod_ver", unit: "", what: "Which version of the catlog mod produced this." },
      { key: "game_build", unit: "", what: "Which build of Kitten Space Agency you were running." },
      {
        key: "install",
        unit: "",
        what: "A random id generated once on this machine. It never leaves your computer in readable form — the server strips it before anything is published.",
      },
    ],
    feeds: [],
    page: "sessions-and-flights",
  },
  {
    type: "flight.started",
    alwaysOn: true,
    family: "flight",
    ver: 1,
    summary: "A vehicle appeared that catlog had not seen before.",
    trigger: "passive",
    cause:
      "Catlog looks over the vehicles in the world twice a second. The first time it sees one, that vehicle's flight begins. A vehicle that splits off another — a decoupled stage, a kitten stepping outside — gets its own flight the same way.",
    source:
      "The vehicle's name, the body it is at, its total mass, how many parts it has, how many stages it was built with, how many seats actually have a kitten in them, who those kittens are, and where on the world it is standing.",
    gate: "Once per vehicle. A save reload resumes the same flight rather than starting a second one.",
    fields: [
      { key: "vehicle_name", unit: "", what: "The vehicle's name, trimmed to 64 characters." },
      { key: "body", unit: "", what: "The world it is at when the flight starts." },
      { key: "mass_kg", unit: "kg", what: "Total mass of the whole vehicle." },
      { key: "part_count", unit: "", what: "How many parts it is built from." },
      {
        key: "crew_count",
        unit: "",
        what: "How many kittens are aboard — occupied seats, not seats fitted.",
      },
      {
        key: "kids",
        unit: "",
        what: "One per-kitten identifier for every kitten aboard, in seat order. Always present: an uncrewed flight carries an empty list rather than nothing at all, so nobody has to tell 'nobody was aboard' apart from 'the mod did not say'. Re-labelled per player before publication, like every other kitten identifier.",
      },
      {
        key: "stage_count",
        unit: "",
        what: "How many stages the vehicle was built with — what the staging list shows in the editor, whether or not you ever fire them all. Zero when it could not be read.",
      },
      {
        key: "lat",
        unit: "°",
        optional: true,
        what: "Where on the world you started, north to south, between −90 and 90. Left out entirely when it could not be read.",
      },
      {
        key: "lon",
        unit: "°",
        optional: true,
        what: "The east-west half of the same position, between −180 and 180, left out under the same rule.",
      },
    ],
    feeds: ["heaviest_launch", "most_parts", "biggest_crew", "biggest_stack"],
    page: "sessions-and-flights",
  },
  {
    type: "flight.ended",
    alwaysOn: true,
    family: "flight",
    ver: 1,
    summary: "A vehicle left the world — recovered, destroyed, or simply gone.",
    trigger: "event",
    cause:
      "The game removes the vehicle. Catlog watches the single point every removal goes through, so recovery, destruction, docking merges and save teardowns all land here exactly once.",
    source:
      "Which of the three endings applies is decided by what the game was doing just before: a recovery, a destruction, or neither.",
    fields: [
      {
        key: "reason",
        unit: "",
        what: "`recovered`, `destroyed`, or `despawned` (merged into another vehicle, or torn down with a save).",
      },
      { key: "crew_count", unit: "", what: "How many kittens were aboard at the end." },
      {
        key: "kids",
        unit: "",
        what: "One per-kitten identifier for every kitten aboard at the end, empty when nobody was.",
      },
      {
        key: "body",
        unit: "",
        what: "The world the flight ended at, or `unknown` on the one path where there is no vehicle left to read it from. Without it the place you came down is unplaceable.",
      },
      {
        key: "lat",
        unit: "°",
        optional: true,
        what: "Where on that world it ended, north to south. Left out entirely when it could not be read.",
      },
      { key: "lon", unit: "°", optional: true, what: "The east-west half of the same position." },
    ],
    feeds: ["kittens_recovered", "biggest_recovery"],
    page: "sessions-and-flights",
  },
  {
    type: "flight.flagged",
    alwaysOn: true,
    family: "flight",
    ver: 1,
    summary: "Something happened on this flight that makes it ineligible for leaderboards.",
    trigger: "event",
    cause:
      "You teleported the vehicle, refilled or drained its resources, used the terminal's destroy command, or changed the kitten locomotion tuning away from stock. The tuning check is the one that is sampled rather than hooked, because it is a value you can drag in a debug window at any time.",
    source:
      "Each flag comes from the exact action that causes it, not from a guess. Normal play never raises one — walking a kitten out of an airlock teleports it internally, and that path is deliberately not flagged.",
    gate: "One event per flag per flight, however many times you do it. A flag raised for the whole session is applied to every flight open at the time and to every flight started afterwards.",
    fields: [
      {
        key: "flag",
        unit: "",
        what: "`teleport`, `refuel`, `resource_edit`, `console`, or `tuning`.",
      },
      { key: "detail", unit: "", what: "A sentence saying exactly what was detected." },
    ],
    feeds: [],
    page: "sessions-and-flights",
  },
  {
    type: "vehicle.situation",
    family: "vehicle",
    ver: 1,
    summary: "The vehicle changed between flying, falling, rolling, landed, floating and so on.",
    trigger: "passive",
    cause: "Sampled twice a second; recorded when the state that was last reported changes.",
    source:
      "The same situation the game itself tracks: whether you are touching terrain, touching water, both, or neither, and whether you are on rails.",
    gate: "At most one change reported every 2 seconds of game time. The first sample after loading only establishes a starting point and reports nothing.",
    fields: [
      { key: "from", unit: "", what: "The state last reported." },
      { key: "to", unit: "", what: "The state now." },
      { key: "body", unit: "", what: "The world you are at." },
      {
        key: "altitude_m",
        unit: "m",
        what: "Height above the body's mean radius — not above the terrain under you.",
      },
      { key: "surface_speed_ms", unit: "m/s", what: "Speed relative to the surface." },
      { key: "orbital_speed_ms", unit: "m/s", what: "Speed relative to the body's centre." },
      {
        key: "radar_alt_m",
        unit: "m",
        optional: true,
        what: "Height above whatever is directly underneath you — the ground or the sea, not the mean radius. Left out entirely when the game has nothing below you to measure against.",
      },
    ],
    feeds: ["softest_touchdown", "landed_bodies", "splashdowns"],
    page: "vehicle",
  },
  {
    type: "vehicle.atmosphere",
    family: "vehicle",
    ver: 1,
    summary: "You entered or left a body's atmosphere.",
    trigger: "passive",
    cause: "Sampled twice a second; recorded when you cross the atmosphere boundary.",
    source:
      "The atmosphere height the game defines for that body, compared against your altitude. A body with no atmosphere has a height of zero, and moving to one counts as leaving.",
    gate: "A 2 % dead band around the boundary, so skimming the edge does not produce a stream of entries and exits. Plus a 2-second minimum between reports in each direction.",
    fields: [
      { key: "dir", unit: "", what: "`entered` or `exited`." },
      { key: "body", unit: "", what: "The world whose atmosphere it is." },
      { key: "speed_ms", unit: "m/s", what: "Surface-relative speed at the crossing." },
      { key: "dyn_pressure_pa", unit: "Pa", what: "Dynamic pressure at the crossing." },
    ],
    feeds: ["fastest_entry"],
    page: "vehicle",
  },
  {
    type: "vehicle.orbit",
    family: "vehicle",
    ver: 1,
    summary: "You achieved a stable orbit, or escaped one.",
    trigger: "passive",
    cause:
      "Sampled twice a second; recorded the moment your orbit clears the bar, and again if you later leave a closed orbit entirely.",
    source:
      "Your periapsis altitude, compared against the top of the atmosphere plus 1000 m. Around an airless body the bar is just 1000 m. Whether the orbit is closed at all is the game's own classification of your trajectory.",
    gate: "Rising edge only — dropping back below the bar re-arms it silently rather than recording anything. 2 seconds minimum between reports.",
    fields: [
      { key: "phase", unit: "", what: "`achieved` or `escaped`." },
      { key: "body", unit: "", what: "The world you are orbiting." },
      { key: "ap_m", unit: "m", what: "Apoapsis altitude. Zero when the orbit is not closed." },
      { key: "pe_m", unit: "m", what: "Periapsis altitude. Can legitimately be negative." },
      { key: "ecc", unit: "", what: "Eccentricity." },
      { key: "inc_deg", unit: "°", what: "Inclination, in degrees." },
      {
        key: "mass_kg",
        unit: "kg",
        what: "What the vehicle weighed at the instant the milestone fired. Beside the mass it started with, this is what is left once the propellant that got you there has been burned.",
      },
    ],
    feeds: [
      "orbits_achieved",
      "highest_apoapsis",
      "lowest_orbit",
      "roundest_orbit",
      "steepest_orbit",
      "heaviest_to_orbit",
      "fastest_to_orbit",
    ],
    page: "vehicle",
  },
  {
    type: "vehicle.soi",
    family: "vehicle",
    ver: 1,
    summary: "You crossed into a different body's sphere of influence.",
    trigger: "passive",
    cause: "Sampled twice a second; recorded when the body you are orbiting changes.",
    source:
      "The parent body the game says you are attached to. If that reading fails, catlog treats it as a failed read rather than as 'left every sphere of influence' — a blank never counts as an arrival.",
    gate: "2 seconds minimum between reports. The first sample after loading only establishes a starting point.",
    fields: [
      { key: "from_body", unit: "", what: "The world you left." },
      { key: "to_body", unit: "", what: "The world you arrived at." },
    ],
    feeds: ["soi_bodies", "fastest_to_<body>"],
    page: "vehicle",
  },
  {
    type: "vehicle.rud",
    family: "vehicle",
    ver: 1,
    summary: "A vehicle came apart — a Rapid Unscheduled Disassembly.",
    trigger: "event",
    cause:
      "The game destroys the vehicle. Catlog is told at the moment of destruction, while the vehicle is still whole, so the speed and altitude it records are the real ones.",
    source:
      "The destruction event itself carries the cause, the peak g and the peak dynamic pressure the vehicle saw. Speed, altitude and the crew count are read off the vehicle a fraction before it goes.",
    fields: [
      {
        key: "cause",
        unit: "",
        what: "`ground_impact`, `ocean_impact`, `collision`, `excessive_g_force`, `aerodynamic_forces` or `hydrodynamic_forces` — whichever the game blamed.",
      },
      { key: "peak_g", unit: "g", what: "The highest g-load the vehicle saw." },
      { key: "peak_q_pa", unit: "Pa", what: "The highest dynamic pressure it saw." },
      { key: "speed_ms", unit: "m/s", what: "Surface speed at the moment of destruction." },
      { key: "altitude_m", unit: "m", what: "Altitude at the moment of destruction." },
      { key: "body", unit: "", what: "Where it happened." },
      {
        key: "crew_count",
        unit: "",
        what: "How many kittens were aboard. They all survive this — the game ends their missions rather than killing them.",
      },
      {
        key: "lat",
        unit: "°",
        optional: true,
        what: "Where on the world it happened, north to south. Left out entirely when it could not be read.",
      },
      { key: "lon", unit: "°", optional: true, what: "The east-west half of the same position." },
    ],
    feeds: ["rud_total", "rud_<cause>"],
    page: "vehicle",
  },
  {
    type: "vehicle.impact",
    family: "vehicle",
    ver: 1,
    summary: "You hit the ground or the water — and whether the vehicle lived through it.",
    trigger: "event",
    cause:
      "The game applies a ground impact or a water splash. Catlog holds the result for one full frame before deciding whether you survived, because destruction is applied after impacts are.",
    source:
      "The game's own impact energy, and for a ground impact its closing speed into the surface. A splash carries no speed of its own, so the number is reconstructed from the energy and the mass.",
    gate: "An impact within 5 seconds of a teleport is discarded entirely — that is not a landing. Destroying the vehicle yourself in the same frame or the next one also flips `survived` to false, so scuttling after a hard landing does not bank a record.",
    fields: [
      {
        key: "speed_ms",
        unit: "m/s",
        what: "How fast you went into the surface. For a ground impact this is the closing speed straight into the terrain, not your total speed.",
      },
      { key: "energy_j", unit: "J", what: "Kinetic energy delivered to the surface." },
      { key: "survived", unit: "", what: "Whether the vehicle still existed a frame later." },
      {
        key: "launch_pad",
        unit: "",
        what: "Whether you hit the launch pad. Pad impacts never score.",
      },
      { key: "body", unit: "", what: "Where it happened." },
      { key: "crew_count", unit: "", what: "How many kittens were aboard." },
      {
        key: "lat",
        unit: "°",
        optional: true,
        what: "Where on the world you hit, north to south. Left out entirely when it could not be read.",
      },
      { key: "lon", unit: "°", optional: true, what: "The east-west half of the same position." },
    ],
    feeds: ["biggest_lithobrake_survived", "biggest_impact_energy"],
    page: "vehicle",
  },
  {
    type: "vehicle.landed",
    family: "vehicle",
    ver: 1,
    summary: "A vehicle touched down on a surface it was not touching before.",
    trigger: "passive",
    cause:
      "Catlog looks at the vehicle twice a second. When it goes from touching nothing — falling, or flying under power — to touching a surface, that is a landing. Every kind of contact counts: settled on the ground, rolling, sailing, floating, dragging, or bottomed out in the shallows.",
    source:
      "The descent rate and the ground-track speed are taken apart from the same velocity the speed boards use, so one is how fast you were coming down and the other is how fast you were moving across. Whether the vehicle lived is not read at all — it is decided a full frame later.",
    gate: "It shares the situation change's 2-second cool-off, so a lander bouncing on touchdown does not mint a record every half second. A vehicle that was already sitting on the ground when you loaded a save does not land: catlog's first look at a vehicle only establishes a starting point and reports nothing. Neither side of the change may be a state catlog does not recognise — not knowing what you were doing is not the same as knowing you were airborne.",
    fields: [
      { key: "body", unit: "", what: "The world you landed on." },
      {
        key: "vertical_speed_ms",
        unit: "m/s",
        what: "How fast you were coming down, counted positive downwards. Smaller is softer.",
      },
      {
        key: "horizontal_speed_ms",
        unit: "m/s",
        what: "How fast you were moving across the ground. Never negative.",
      },
      { key: "crew_count", unit: "", what: "How many kittens were aboard." },
      {
        key: "survived",
        unit: "",
        what: "Whether the vehicle was still in one piece a full frame later.",
      },
      {
        key: "radar_alt_m",
        unit: "m",
        optional: true,
        what: "Height above the ground at the moment catlog noticed. Not expected to be exactly zero — catlog looks twice a second, so the reading can be up to half a second after the wheels touched. Left out entirely when there was nothing below to measure against.",
      },
      {
        key: "lat",
        unit: "°",
        optional: true,
        what: "Where on the world you came down, north to south. Left out entirely when it could not be read.",
      },
      { key: "lon", unit: "°", optional: true, what: "The east-west half of the same position." },
    ],
    feeds: ["softest_landing", "landings"],
    page: "vehicle",
  },
  {
    type: "vehicle.staging",
    family: "vehicle",
    ver: 1,
    summary: "You pressed the stage key and the next stage fired.",
    trigger: "event",
    cause:
      "The game activates the next sequence on the vehicle. There is exactly one path to that, and it is behind the stage key.",
    source: "The index of the stage that just became active.",
    fields: [
      { key: "stage_index", unit: "", what: "Which stage is now active, counting from zero." },
    ],
    feeds: ["stagings", "most_stages"],
    page: "vehicle",
  },
  {
    type: "vehicle.docked",
    family: "vehicle",
    ver: 1,
    summary: "Two vehicles docked.",
    trigger: "event",
    cause:
      "A docking port completes a dock. Catlog hooks the port rather than the physics event, so player-commanded docking counts as well as automatic docking.",
    source:
      "Both vehicles are registered first, so each already has its own flight when the dock is recorded.",
    fields: [
      {
        key: "other_flight",
        unit: "",
        what: "The other vehicle's flight, when it has one. Blank when catlog was not tracking a flight for it.",
      },
    ],
    feeds: ["dockings"],
    page: "vehicle",
  },
  {
    type: "vehicle.undocked",
    family: "vehicle",
    ver: 1,
    summary: "Two vehicles undocked.",
    trigger: "event",
    cause: "A docking port splits the vehicle in two.",
    source:
      "The piece that split off is registered as its own flight before the undock is recorded.",
    fields: [{ key: "other_flight", unit: "", what: "The flight of the piece that split off." }],
    feeds: [],
    page: "vehicle",
  },
  {
    type: "engine.ignition",
    family: "engine",
    ver: 1,
    summary: "The vehicle's engines lit.",
    trigger: "passive",
    cause:
      "Sampled twice a second. Recorded when the vehicle goes from no engines running to at least one.",
    source:
      "Whether any engine on the vehicle is active, plus how many are and the type of the first one found. This is a whole-vehicle reading, not a per-engine one.",
    gate: "Whole-vehicle. Shutting down one of two engine groups records nothing until the last one stops. A vehicle already burning when you load a save does not report a fresh ignition.",
    fields: [
      { key: "engine", unit: "", what: "The engine type, or `unknown` when it could not be read." },
      { key: "count", unit: "", what: "How many engines are running." },
    ],
    feeds: ["engine_ignitions"],
    page: "engines",
  },
  {
    type: "engine.shutdown",
    family: "engine",
    ver: 1,
    summary: "The vehicle's last running engine stopped.",
    trigger: "passive",
    cause:
      "Sampled twice a second. Recorded when the vehicle goes from at least one engine running to none.",
    source:
      "The same whole-vehicle reading as ignition. `count` here is how many were running a moment ago.",
    gate: "Whole-vehicle, exactly as ignition.",
    fields: [
      { key: "engine", unit: "", what: "The engine type, or `unknown`." },
      { key: "count", unit: "", what: "How many were running before they stopped." },
    ],
    feeds: [],
    page: "engines",
  },
  {
    type: "engine.flameout",
    family: "engine",
    ver: 1,
    summary: "The engines are still on but the propellant ran out.",
    trigger: "passive",
    cause:
      "Sampled twice a second. Recorded when engines are running and propellant availability goes from yes to no in the same tick that no start or stop happened.",
    source:
      "The game has no flameout of its own — this is reconstructed from two whole-vehicle readings: is anything running, and is any propellant available.",
    gate: "Only when the ignition/shutdown edge did not already fire this tick.",
    fields: [
      { key: "engine", unit: "", what: "The engine type, or `unknown`." },
      { key: "count", unit: "", what: "How many engines are running." },
    ],
    feeds: ["flameouts"],
    page: "engines",
  },
  {
    type: "kitten.eva_start",
    family: "kitten",
    ver: 1,
    summary: "A kitten stepped outside.",
    trigger: "event",
    cause:
      "An EVA door produces a kitten. If the door cannot — no backpack — nothing is recorded, because no egress happened.",
    source: "The kitten's name comes from the roster entry the door was handed.",
    fields: [
      {
        key: "kid",
        unit: "",
        what: "A per-kitten identifier. It is re-labelled per player before publication.",
      },
      { key: "name", unit: "", what: "The kitten's name, trimmed to 32 characters." },
    ],
    feeds: ["evas"],
    page: "kittens",
  },
  {
    type: "kitten.eva_end",
    family: "kitten",
    ver: 1,
    summary: "A kitten's EVA finished.",
    trigger: "event",
    cause: "The kitten-outside vehicle is removed — boarded, recovered, or otherwise gone.",
    source: "The duration is measured from when the EVA vehicle was created to now, in game time.",
    fields: [
      { key: "kid", unit: "", what: "The per-kitten identifier." },
      { key: "name", unit: "", what: "The kitten's name." },
      { key: "duration_s", unit: "s", what: "How long the kitten was outside, in game seconds." },
    ],
    feeds: ["longest_eva"],
    page: "kittens",
  },
  {
    type: "kitten.tumble",
    family: "kitten",
    ver: 1,
    summary: "A kitten went over.",
    trigger: "passive",
    cause:
      "Sampled twice a second. Recorded when a kitten's movement state changes into tumbling. Only transitions *into* tumbling count — a tumble ends by righting itself, and counting that too would double every fall.",
    source:
      "The game decides a kitten is tumbling when it is touching the ground and moving faster than a tuning value that is 6.5 m/s in a stock game. Changing that value marks the flight — and a tumble belongs to the kitten's own spacewalk flight, so a marked flight's tumbles do not count.",
    gate: "One event per fall. The speed reported is the kitten's ground speed at the moment it went over. If catlog cannot tell which flight the kitten was on, the tumble is recorded without one and counts as it always did.",
    fields: [
      { key: "kid", unit: "", what: "The per-kitten identifier." },
      { key: "name", unit: "", what: "The kitten's name." },
      { key: "speed_ms", unit: "m/s", what: "Ground speed when it went over." },
      { key: "body", unit: "", what: "Where it happened." },
    ],
    feeds: ["kitten_tumbles"],
    page: "kittens",
  },
  {
    type: "kitten.kia",
    alwaysOn: true,
    family: "kitten",
    ver: 1,
    summary: "A kitten was lost.",
    trigger: "passive",
    cause:
      "Catlog compares the roster against its last reading twice a second and notices a kitten newly marked as lost. Loading a save that already contains losses does not replay them.",
    source:
      "The roster's own killed-in-action flag, which the game sets in exactly one place and never clears. Whether you did it deliberately is decided by whether the game's kill-the-crew path ran in the previous 2 seconds — that path only runs when a player asks for it. The same moment is what tells catlog which flight she was on: it reads the seats while the vehicle still exists, because a moment later the roster knows only a name.",
    gate: "Once per kitten. The flag is never reset, so a kitten cannot be lost twice. The loss names a flight only when catlog can prove which one — otherwise it is recorded without one, deliberately, because a wrong flight would strike a record off somebody else's landing. Without a flight named, the rule that drops a crash which killed a kitten cannot reach it.",
    fields: [
      { key: "kid", unit: "", what: "The per-kitten identifier." },
      { key: "name", unit: "", what: "The kitten's name." },
      {
        key: "context",
        unit: "",
        what: "`manual_destroy` when you asked for it, `unknown` otherwise.",
      },
    ],
    feeds: [],
    page: "kittens",
  },
  {
    type: "roster.snapshot",
    family: "roster",
    ver: 1,
    summary: "Where every kitten in your roster stands.",
    trigger: "passive",
    cause:
      "Every 10 minutes of game time — so under time warp, far more often in real minutes — and once more when the game shuts down.",
    source: "The roster's own running totals for each kitten.",
    gate: "Skipped entirely when the roster is empty. A save reload does not emit a closing snapshot for the session that just ended.",
    fields: [
      { key: "kittens[].kid", unit: "", what: "The per-kitten identifier." },
      { key: "kittens[].name", unit: "", what: "The kitten's name." },
      { key: "kittens[].travelled_m", unit: "m", what: "Lifetime distance travelled." },
      {
        key: "kittens[].fastest_ms",
        unit: "m/s",
        what: "The roster's fastest-speed figure. It is measured against the solar system rather than the body you are at, so it reads about 30 km/s while you stand still on Earth. Recorded for completeness; it is never used for a speed board.",
      },
      {
        key: "kittens[].missions",
        unit: "",
        what: "Mission count. Aborted pre-launch missions count too.",
      },
      {
        key: "kittens[].mission_time_s",
        unit: "s",
        what: "Total mission time. It only updates at mission boundaries, so it lags mid-flight.",
      },
      { key: "kittens[].kia", unit: "", what: "Whether this kitten has been lost." },
    ],
    feeds: ["distance_travelled", "top_kitten_distance", "top_kitten_missions"],
    page: "kittens",
  },
  {
    type: "telemetry.window",
    family: "telemetry",
    ver: 1,
    summary: "Thirty seconds of flight, summarised.",
    trigger: "passive",
    cause:
      "This is the background telemetry. Catlog samples each live vehicle twice a second and folds 30 game-seconds of samples into one summary — 60 samples in a full window.",
    source:
      "Altitude above mean radius, surface speed, orbital speed and acceleration, each as a minimum, maximum, mean and last value. Height above the ground is folded the same way, over only the samples that had a ground reading. Peak g and peak dynamic pressure are included only when the game actually computed them.",
    gate: "A window also closes early when the flight ends, when a vehicle disappears, or when the game shuts down — those windows are short. Loading a save discards the partial window rather than folding two timelines together.",
    fields: [
      { key: "t0_sim", unit: "s", what: "Game time of the first sample." },
      { key: "t1_sim", unit: "s", what: "Game time of the last sample." },
      { key: "n", unit: "", what: "How many samples were folded. A full window is 60." },
      { key: "body", unit: "", what: "Where you were at the end of the window." },
      { key: "alt_m", unit: "m", what: "Altitude — min, max, mean, last." },
      { key: "surface_speed_ms", unit: "m/s", what: "Surface speed — min, max, mean, last." },
      { key: "orbital_speed_ms", unit: "m/s", what: "Orbital speed — min, max, mean, last." },
      { key: "accel_ms2", unit: "m/s²", what: "Acceleration — min, max, mean, last." },
      {
        key: "peak_g",
        unit: "g",
        optional: true,
        what: "Highest g-load in the window. Left out entirely — not set to zero — when the game did not compute one, which is the case whenever your vehicle is on rails or in freefall.",
      },
      {
        key: "max_q_pa",
        unit: "Pa",
        optional: true,
        what: "Highest dynamic pressure in the window, left out under the same rule.",
      },
      { key: "mass_kg_last", unit: "kg", what: "Mass at the end of the window." },
      {
        key: "radar_alt_m",
        unit: "m",
        optional: true,
        what: "Height above the ground directly underneath — min, max, mean, last. Folded over only the samples that had a ground reading, and left out entirely when none of them did, exactly as `peak_g` is. So its samples are not the same population as `n`, and a window spent in orbit carries no such reading at all.",
      },
      {
        key: "warp_max",
        unit: "",
        what: "The highest time-warp speed during the window; `1` is real time. It says how much game happened per sample, so a reader can tell a full 60-sample window from the handful a deep warp leaves behind. Descriptive only — it disqualifies nothing and is not a cheat signal.",
      },
    ],
    feeds: [
      "peak_g_survived",
      "max_q_survived",
      "fastest_surface_speed",
      "fastest_orbital_speed",
      "highest_altitude",
      "lowest_pass",
    ],
    droppable: true,
    page: "telemetry",
  },
];

export const FAMILY_LABEL: Record<Family, string> = {
  system: "Systems",
  session: "Session",
  flight: "Flight",
  vehicle: "Vehicle",
  engine: "Engines",
  kitten: "Kittens",
  roster: "Roster",
  telemetry: "Telemetry",
};

export const TRIGGER_LABEL: Record<Trigger, string> = {
  event: "Something happened",
  passive: "Sampled in the background",
};

export function eventByType(type: string): CatlogEvent | undefined {
  return EVENTS.find((e) => e.type === type);
}

/** The events that cannot be switched off, in catalog order. */
export const ALWAYS_ON: CatlogEvent[] = EVENTS.filter((e) => e.alwaysOn === true);
