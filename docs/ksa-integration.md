# §1.3 Event detection — patch points (re-verified against build **2026.8.5.5168**)

Drop-in replacement for `the original proposals (now removed)` §1.3 (lines ~66–97).

- **CURRENT** decomp: `/Users/asherwin/repos/meow-sci/ksa-game-assemblies/current/decomp` — build `2026.8.5.5168` (`current/version.json`, note: *not* `../version.json`).
- **PREVIOUS** decomp available for diffing: `/Users/asherwin/repos/meow-sci/ksa-game-assemblies_prev/current/decomp` — build `2026.8.3.5117`.
- The plan was verified against `2026.7.3.4826`, which is **not** on disk. Where a claim is marked "new this build" it means new in 5168 vs 5117; anything the plan asserted that only appeared in 5168 was therefore unverifiable when written.

All file paths below are absolute and rooted at
`/Users/asherwin/repos/meow-sci/ksa-game-assemblies/current/decomp/KSA/…` (abbreviated `KSA/…`).

---

## 0. Breaking changes vs the plan — what an implementer must do differently

Only the items that change code you would otherwise write.

| # | Plan said | Reality in 5168 | Do this instead |
|---|---|---|---|
| **B1** | `EVADoor.CreateKittenEva` (private) | **Two different methods share the name.** `KittenEva.CreateKittenEva(CelestialSystem, VehicleTemplate, IParentBody, string)` is **public static**; `EVADoor.CreateKittenEva(Vehicle, IVASeat, KittenRosterEntryData)` is **private instance** returning `KittenEva?`. | Disambiguate by declaring type in `AccessTools`. Patch **`EVADoor.CreateKittenEva`** for in-flight EVA egress; the `KittenEva` static one is the scenario/template spawn path and its `id` is **not** guaranteed to be a roster name. |
| **B2** | `Parts.RocketNozzles` → `ActivatedThisFrame` / `DeactivatedThisFrame` | Those flags are **not on the module**. `RocketNozzles` is a `ModuleStateful<RocketNozzle, RocketNozzleState, EmptyStruct, RocketNozzleFxState>.StateList` **field**; the flags are `required bool` **fields on the `RocketNozzleState` struct** in the `States` span. Enumerators are `ref struct` — not `IEnumerable`. | Iterate with the manual `GetEnumerator()/MoveNext()/.Current` pattern over `.ModulesAndStates`, read `current.State.ActivatedThisFrame`. Do **not** `foreach` with LINQ, do not store spans across frames. |
| **B3** | "flameout = deactivation while controller `IsActive`" | **There is no flameout concept anywhere in the codebase** (zero hits for `flameout`/`starved`/`ResourceAvailable`). Also `RocketCore.Controller` is typed **`IActivate`**, not `EngineController` — it may be a `ThrusterController` (RCS). | Use the game's own predicate: `IsActive == true && state.IsPropellantAvailable == false`. Reach the controller via `nozzle.Rocket?.Core?.Controller` (3 nullable hops) and type-test `is EngineController` to exclude RCS. |
| **B4** | Ap/Pe "are radii… NaN-guard everything" | Radii ✔, but on **hyperbolic** orbits `Apoapsis` is **negative**, not NaN (`Period` *is* NaN). Only **parabolic** yields `Apoapsis == NaN`. There are **no** `*Altitude` orbit properties. | Branch on `Orbit.IsHyperbolic()/IsParabolic()/IsBound()`, not on NaN sniffing. Subtract `Orbit.Parent.MeanRadius` yourself for altitudes. |
| **B5** | Roster stats implied numeric | `TravelledMeters` and `FastestSpeed` are **`DistanceReference` (a class)**, not `double`; `TotalMissionTime` is **`SimTimeReference` (a class)**. | Read `.InMeters()` / `.GetSeconds()`. Also: `TotalMissionTime` only banks at mission **boundaries** (stale mid-mission), and `FastestSpeed` is **ecliptic-frame** speed (includes the parent body's orbital motion) — do not present it as a vehicle speed record. |
| **B6** | `vehicle.Parent` — implicitly safe | `Vehicle.Parent => Orbit.Parent` chains through `FlightPlan.Patches[0]`; an uninitialized vehicle **throws `ArgumentOutOfRangeException`**, it does not return null. | Guard `vehicle.FlightPlan.Patches.Count > 0` before touching `Orbit`/`Parent`. `Parent.Id` is `string` (from `IObjectId`); `Parent.Hash` is `KeyHash` (a `readonly record struct` over `uint`) and is the cheap comparison key. |
| **B7** | `Vehicle.TeleportToLocation` as an assist marker | `TeleportToLocation` **does not teleport** — it enqueues `InputEvents.TeleportInputData`. | Hook `Vehicle.Teleport(Orbit?, doubleQuat?, double3?)` (the real mutation) and/or `InputEvents.TeleportInputData.Apply()`. `TeleportToLocation` alone will miss console/UI teleports. |
| **B8** | `Universe.KittenRoster` snapshot | Correct, but the whole `KittenRosterData` **object is swapped** on save-load (`Universe.cs:2178`) and new-game (`Universe.cs:176`). | Never cache the roster object or entry references across a load; re-resolve from `Universe.KittenRoster.Kittens` each sample. |
| **B9** | Tumble gate treated as a constant | `KittenLocomotionTuning.Current` is a **public static mutable field**, and the game ships a debug window (`KittenTuningWindow`, "Kitten Locomotion Tuning") that live-edits `TumbleSpeedGate` via `ImGui.DragFloat`. | Read `KittenLocomotionTuning.Current.TumbleSpeedGate` at sample time and **flag the flight as tuned** when it != `6.5f`. Otherwise tumble records are trivially forgeable. |
| **B10** | `Vehicle.StructuralLoad.PeakGLoad` as an available polled surface | `StructuralLoad` is **brand new in 5168** (did not exist in 5117) — so this was unverifiable when the plan was written. It is also **only written inside full physics** and reset to `default` every prepared step. | Treat all-zero `StructuralLoad` as "no data this step" (on-rails / freefall vehicle), not as "zero g". |
| **B11** | `GroundImpactEvent.ImpactVelocity` | **New in 5168** (r5162). It did not exist in 5117. | Fine to use — but the `[KsaAnchor]` on it must be tight; it is one build old. |
| **B12** | `CelestialSystem.Register` "deregister = removal choke point" | `CelestialSystem.Deregister(Astronomical)` exists, but the actual single funnel for *all* vehicle removal is **`Vehicle.Dispose(bool endMission)`** (`Vehicle.cs:3510`), which calls `Deregister` at `Vehicle.cs:3520`. `Register` is also called on **rename** (deregister → rename → register), which will look like a remove+add. | Hook `Vehicle.Dispose` for removal; special-case `CelestialSystem.Rename`. |

**Everything else in §1.3 verified unchanged.** In particular the two load-bearing claims — the impact-before-destruction ordering and the "physics RUD does not kill crew" claim — are both **confirmed true in 5168** (details in §3 and §4).

---

## 1. Polled surfaces

| Row | Status | Exact current signature | file:line | Note |
|---|---|---|---|---|
| `Vehicle.Situation` — 8 values incl. `Dragging`, `Bottomed` | **VERIFIED** | `public enum Situation : byte { Freefall = 1, Maneuvering = 0, Rolling = 2, Landed = 3, Sailing = 4, Floating = 5, Dragging = 6, Bottomed = 7 }` | `KSA/Situation.cs:3-13` | Byte-identical to 5117. Accessor: `public Situation Situation => _props.Situation;` `KSA/Vehicle.cs:533`. |
| `Situation` is a packed bitfield | **NEW INFO** | `GetSurfaceContact() => (SurfaceContact)((int)sit >> 1)`; `IsOnRails() => EnumEx.IsSet((int)sit, 1)` | `KSA/SituationEx.cs:56`, `:62` | Value = `(SurfaceContact << 1) \| onRailsBit`. `SurfaceContact { None, Terrain, Ocean, TerrainAndOcean }` (`KSA/SurfaceContact.cs:3-9`). So `Dragging`=Terrain+Ocean off-rails, `Bottomed`=Terrain+Ocean on-rails. |
| `SituationEx` helper methods | **VERIFIED** | `IsSet`, `HasAnyContact`, `HasTerrainContact`, `HasOceanContact`, `IsFreeInOcean`, `IsAsleepOnTerrain`, `IsAsleepOnSurface`, `GetSurfaceContact`, `IsOnRails`, `WithSurfaceContact`, `WithTerrainContact`, `WithOceanContact`, `WithOnRails`, `FromComponents` — all `public static` extension methods | `KSA/SituationEx.cs:5-102` | Also `public const byte OffRailsFlag = 0; OnRailsFlag = 1;` (`:7`, `:9`). Plan's advice to use helpers over exhaustive switches is correct. |
| `GetBarometricAltitude()` | **VERIFIED** | `public double GetBarometricAltitude()` → `Orbit.StateVectors.PositionCci.Length() - Orbit.Parent.MeanRadius` | `KSA/Vehicle.cs:2840-2843` | Altitude above **mean radius**, not terrain. Terrain-relative is `public double GetRadarAltitude()` `KSA/Vehicle.cs:2845`. |
| `Parent.GetAtmosphereReference().Physical.Height` | **VERIFIED (chain is nullable)** | `AtmosphereReference? GetAtmosphereReference()` `KSA/IParentBody.cs:57`, impl `public AtmosphereReference? GetAtmosphereReference()` `KSA/Astronomical.cs:332`; `public PhysicalAtmosphereReference Physical = …` (**field**) `KSA/AtmosphereReference.cs:11`; `public DistanceReference Height => _height;` `KSA/PhysicalAtmosphereReference.cs:23` | as listed | `Height` is a **`DistanceReference` class** → cast `(double)` (implicit op `KSA/DistanceReference.cs:63`) or `.InMeters()` (`:148`). Game pattern: `parent.GetAtmosphereReference()?.Physical.Height` `KSA/Vehicle.cs:2886`. Prefer the ready-made `double GetAtmosphereRadius()` `KSA/IParentBody.cs:61` (returns `MeanRadius + Height`, or `0.0` if no atmosphere). |
| Eccentricity | **VERIFIED** | `public double Eccentricity => _data.Eccentricity;` | `KSA/Orbit.cs:1154` | Always valid. |
| Apoapsis / Periapsis — **radii?** | **VERIFIED as radii** | `public double Periapsis => _data.Periapsis;` `KSA/Orbit.cs:1166`; `public double Apoapsis => _data.Apoapsis;` `KSA/Orbit.cs:1168`; also re-exposed `public double Periapsis => Orbit.Periapsis;` `KSA/Vehicle.cs:383` and `Apoapsis` `:385` | as listed | **Confirmed radii from body center.** Computed `a(1±e)` at `KSA/Orbit.cs:1573-1574`; every UI site subtracts `MeanRadius` (`KSA/IOrbiter.cs:105-106`, `KSA/Program.cs:2438`, `:2452`); impact test is `periapsis < meanRadius` (`KSA/Vehicle.cs:2890`). **No `*Altitude` properties exist.** |
| Ap/Pe on non-elliptical orbits | **CHANGED vs plan's "NaN-guard"** | hyperbolic: `apoapsis = a(1+e)` with `a < 0` ⇒ **negative**; `period = NaN`. parabolic: `apoapsis = NaN`, `semiMajorAxis = +Inf`. | `KSA/Orbit.cs:1578-1595`; struct fallbacks `KSA/OrbitData.cs:61-75` | Game's own guard: `if (num2 < 0.0) num2 = double.PositiveInfinity;` `KSA/Vehicle.cs:2879-2883`. Use `Orbit.IsHyperbolic()/IsParabolic()/IsBound()` (`KSA/Orbit.cs:1751-1775`). |
| Inclination | **VERIFIED** | `public double Inclination => _data.Inclination;` | `KSA/Orbit.cs:1160` | **Radians.** Siblings: `SemiMajorAxis` `:1156`, `SemiMinorAxis` `:1158`, `LongitudeOfAscendingNode` `:1162`, `ArgumentOfPeriapsis` `:1164`, `Period` (s) `:1170`, `TimeAtPeriapsis` `:1152`. |
| Anomalies | **GONE (never existed as properties)** | `public TrueAnomaly GetTrueAnomaly()` `KSA/Orbit.cs:2193`; `public abstract MeanAnomaly GetMeanAnomaly(TrueAnomaly ta)` `KSA/Orbit.cs:1302` | as listed | `TrueAnomaly`/`MeanAnomaly` are `readonly struct` wrappers — use `.Degrees` (`KSA/TrueAnomaly.cs:21`). |
| SOI: `vehicle.Parent.Id`, type of `Id` | **VERIFIED — `Id` is `string`** | `public IParentBody Parent => Orbit.Parent;` `KSA/Vehicle.cs:363`; `string Id { get; }` `KSA/IObjectId.cs:5`; impl `public virtual string Id { get; protected set; }` `KSA/Astronomical.cs:96` | as listed | `Id` is **not** declared on `IParentBody` — it comes from `IObjectId`. Cheap key: `KeyHash Hash { get; }` `KSA/IObjectId.cs:7` (`public readonly record struct KeyHash(uint Code)` `KSA/KeyHash.cs:9`). **See B6: `Parent` can throw, not return null.** |
| `Parts.RocketNozzles` | **CHANGED (shape clarified)** | `public ModuleStateful<RocketNozzle, RocketNozzleState, EmptyStruct, RocketNozzleFxState>.StateList RocketNozzles;` — **public instance field** | `KSA/PartTree.cs:49` (was `:47` in 5117 — line shift only) | `RocketNozzle` is `public abstract class RocketNozzle : ModuleStateful<…>` `KSA/RocketNozzle.cs:9`. Enumerate via `.ModulesAndStates` / `.ModulesAndAllStates` (`ref struct` enumerators, `KSA/ModuleStateful.cs:266-281`); `.NumModules` for count. Real usage: `KSA/Vehicle.cs:3946`, `:5203`. |
| `ActivatedThisFrame` / `DeactivatedThisFrame` | **CHANGED (location)** | `public required bool ActivatedThisFrame;` / `public required bool DeactivatedThisFrame;` — **fields on `public struct RocketNozzleState`** | `KSA/RocketNozzleState.cs:28`, `:30` | Cleared `KSA/RocketCore.cs:172-173`, set `:205`/`:210`, solid-motor override `KSA/SolidMotor.cs:206-207`, copied core→nozzle `KSA/RocketNozzle.cs:218-219`. **Caveat:** `Rocket.UpdateRockets` early-outs when all cores are idle (`KSA/Rocket.cs:88-91`), so a stale `true` can persist. Treat as an edge hint, debounce. |
| Engine "controller IsActive" reachability | **CHANGED (3 nullable hops, `IActivate`)** | `public bool IsActive { get; internal set; } = false;` on `public class EngineController : ModuleStateful<…>, IActivate` | `KSA/EngineController.cs:36` (class `:7`) | Path: `nozzle.Rocket` (`public Rocket Rocket = null;` `KSA/RocketNozzle.cs:34`, **nullable**) → `Rocket.Core` (`public required RocketCore Core;` `KSA/Rocket.cs:10`) → `RocketCore.Controller` (**`public IActivate Controller = null;`** `KSA/RocketCore.cs:18`). `Controller` may be a `ThrusterController` (RCS) — type-test. `public void SetIsActive(Vehicle? vehicle, bool activationState)` `KSA/EngineController.cs:67` is **deferred** (enqueues `InputEvents.IActivateInputData`, applied at `KSA/InputEvents.cs:339-340`). |
| Flameout | **GONE (never existed)** | — | — | See **B3**. Use `IsActive && !state.IsPropellantAvailable` (`public required bool IsPropellantAvailable;` `KSA/EngineControllerState.cs:7`; game's own test `KSA/EngineController.cs:60`). Whole-vehicle helpers exist: `Vehicle.IsAnyEngineActive()` / `IsAnyEnginePropellantAvailable()` (used at `KSA/Vehicle.cs:6090-6094`). |
| `KittenEva.LocomotionState.Mode` | **VERIFIED — but the entire subsystem is NEW in 5168** | `public LocomotionState LocomotionState => _locomotionState;` (public get-only property returning a **struct copy**) | `KSA/KittenEva.cs:20` | `LocomotionState.cs`, `LocomotionMode.cs`, `LocomotionFacts.cs`, `LocomotionCommand.cs`, `KittenLocomotion.cs`, `KittenLocomotionTuning.cs` **did not exist in 5117**. `public struct LocomotionState { public LocomotionMode Mode; public double ModeStartTime; … }` `KSA/LocomotionState.cs:5-32`. Written back from worker results at `KSA/KittenEva.cs:173-177`. |
| `LocomotionMode` enum values | **VERIFIED — 6 values** | `public enum LocomotionMode : byte { Mmu, Grounded, Airborne, Tumbling, Rightening, Ladder }` | `KSA/LocomotionMode.cs:3-11` | `Rightening` is the post-tumble settle state (Tumbling→Rightening→Grounded). `Ladder` is **declared but unreachable** — changelog r5161: "Imported Core Utility A (ladders). Not yet functional as ladders (SoonTM)." |
| Tumble speed gate = 6.5 m/s | **VERIFIED — and it is a mutable static, not a constant** | `public float TumbleSpeedGate;` `KSA/KittenLocomotionTuning.cs:33`; `TumbleSpeedGate = 6.5f` in `public static KittenLocomotionTuning Default =>` `KSA/KittenLocomotionTuning.cs:77`; `public static KittenLocomotionTuning Current = Default;` `KSA/KittenLocomotionTuning.cs:59` | as listed | Changelog r5131 confirms "Increased TumbleSpeedGate to 6.5ms from 5.5ms". **See B9** — `KittenTuningWindow` (`KSA/KittenTuningWindow.cs:9`, instantiated `KSA/Program.cs:3422`) live-edits `KittenLocomotionTuning.Current` fields by `ref`. |
| Tumble classification rule | **VERIFIED** | `if (facts.TerrainContact && facts.TangentialSpeedPhys >= tuning.TumbleSpeedGate) return LocomotionMode.Tumbling;` | `KSA/KittenLocomotion.cs:30-33` (in `public static LocomotionMode DeriveMode(...)` `:24`) | Fires from **any** mode except `Ladder` (early-return `:26-29`). Feet-first Airborne→Grounded is the `flag` branch at `:44-47`. `TangentialSpeedPhys` is the **body-fixed (CCF) tangential** component, computed at `KSA/VehicleUpdateTask.cs:1154` (`BuildKittenLocomotionFacts`), not raw speed. |
| `Universe.KittenRoster` | **VERIFIED** | `public static KittenRosterData KittenRoster { get; private set; } = new KittenRosterData();` | `KSA/Universe.cs:94` | Never null, but **swapped wholesale** on load (`:2178`) / new game (`:176`) — see **B8**. |
| Roster enumeration | **VERIFIED** | `public List<KittenRosterEntryData> Kittens = new List<KittenRosterEntryData>();` — **public field, a flat `List<>`** | `KSA/KittenRosterData.cs:13-14` | `foreach (KittenRosterEntryData k in Universe.KittenRoster.Kittens) { … }`. Lookup is a linear scan: `public KittenRosterEntryData? Find(KeyHash nameHash)` `:77`, `Find(string name)` `:89`. |
| `TravelledMeters` | **CHANGED (type)** | `public DistanceReference TravelledMeters = new DistanceReference(0.0);` | `KSA/KittenRosterEntryData.cs:32` | `DistanceReference` is a **class** — use `.InMeters()`. Written only via `AddTravelledMeters` (`:112-115`), fed from `Vehicle.CreditCrewTravelStats` (`KSA/Vehicle.cs:2813`) ← `UpdatePerFrameData` (`KSA/Vehicle.cs:2468`). |
| `FastestSpeed` | **CHANGED (type + semantics)** | `public DistanceReference FastestSpeed = new DistanceReference(0.0);` | `KSA/KittenRosterEntryData.cs:35` | Stores **m/s in a distance wrapper**. Monotonic max via `CreditSpeed` (`:117-123`). **Source is `_velocityEcl.Length()`** (`KSA/Vehicle.cs:2468`) — ecliptic frame, so it includes the parent body's heliocentric motion. Expect ~30 km/s baseline on Earth. Not a usable "vehicle speed record" without reframing. |
| `MissionCount` | **VERIFIED** | `public int MissionCount;` | `KSA/KittenRosterEntryData.cs:26` | Only write: `MissionCount++` in `StartMission` (`:78`). Counts aborted pre-launch missions too. |
| `TotalMissionTime` | **CHANGED (type + staleness)** | `public SimTimeReference TotalMissionTime = new SimTimeReference();` | `KSA/KittenRosterEntryData.cs:23` | Class; `.GetSeconds()`. Only advanced by `private void BankElapsedTime()` (`:125-132`), called from `StartMission`/`EndMission` only. **Live value** = `TotalMissionTime.GetSeconds() + (Universe.GetElapsedSeconds() - CurrentMissionStartTime.GetSeconds())` when `AssignedToVehicle`. |
| `Kia` | **VERIFIED** | `[XmlAttribute("KIA")] public bool Kia;` | `KSA/KittenRosterEntryData.cs:28-29` | Set in exactly one place: `Kia = true;` `KSA/KittenRosterEntryData.cs:108` inside `public void Kill(bool hasLaunched)` `:96`. **Never reset to false.** Full kill-path analysis in §4. |
| `Universe.GetElapsedSimTime()` | **VERIFIED** | `public static SimTime GetElapsedSimTime()` | `KSA/Universe.cs:2109` | Companion `public static double GetElapsedSeconds()` `KSA/Universe.cs:2103`. `Universe` is `public static class Universe`. |
| `Vehicle.LaunchGameTime` — set at construction, survives save/load | **VERIFIED (all three claims)** | `public SimTime LaunchGameTime = SimTime.Zero;` — **public instance field** | `KSA/Vehicle.cs:162` | Set at ctor: `LaunchGameTime = Universe.GetElapsedSimTime();` `KSA/Vehicle.cs:1313`. Restored from save: `LaunchGameTime = vehicleData.LaunchGameTime;` `:922`. Serialized: `LaunchGameTime = new SimTimeReference(LaunchGameTime)` `:1026`. **Inherited by split children:** `vehicle.LaunchGameTime = LaunchGameTime;` `:1543`. |
| `Vehicle.TotalMass` | **VERIFIED (note: `float`)** | `public float TotalMass => _props.TotalMassPropsAsmb.Props.Mass;` | `KSA/Vehicle.cs:551` | Siblings `InertMass` `:553`, `PropellantMass` `:555`. |
| Part count | **VERIFIED** | `public int Count => Parts.Length;` on `PartTree` | `KSA/PartTree.cs:89` | `vehicle.Parts.Count`. `public ReadOnlySpan<Part> Parts => …` `:91`. `Vehicle.Parts` is a full property `KSA/Vehicle.cs:589`. |
| Crew count | **CHANGED (semantics)** | `public ReadOnlySpan<IVASeat> Crew => Parts.Modules.Get<IVASeat>();` `KSA/Vehicle.cs:373`; `public int SeatCount => (this is KittenEva) ? 1 : Parts.Modules.Get<IVASeat>().Length;` `KSA/Vehicle.cs:375` | as listed | **`Crew` is ALL seats, not occupants.** Occupied ⇔ `seat.AssignedKittenHash != KeyHash.Zero` (`public KeyHash AssignedKittenHash;` `KSA/IVASeat.cs:46`; game's own test `KSA/IVASeat.cs:96-109`). No `CrewCount` property exists. |
| Surface vs orbital speed | **VERIFIED** | `public double GetSurfaceSpeed()` `KSA/Vehicle.cs:2759`; `public double GetInertialSpeed()` `KSA/Vehicle.cs:2754`; `public double OrbitalSpeed => GetVelocityCci().Length();` `KSA/Vehicle.cs:581` | as listed | **No `Vehicle.GetOrbitalSpeed()`** — `Orbit.GetOrbitalSpeed(double radiusMeters)` `KSA/Orbit.cs:1422` is a vis-viva helper, not current speed. Do **not** use `NavBallData.Speed` (`KSA/Vehicle.cs:575`) — it is frame-dependent (switch at `KSA/Vehicle.cs:2506-2590`). |
| Dynamic pressure | **CHANGED (no property)** | `public static double GetDynamicPressure(Vehicle? vehicle)` | `KSA/PhysicalAtmosphereReference.cs:66` | **No `Vehicle.DynamicPressure`.** Game calls it as `PhysicalAtmosphereReference.GetDynamicPressure(this)` (`KSA/Vehicle.cs:5672`). Cheaper cached alternatives: `vehicle.PhysicsEnvironment.AtmosphericPressure` / `.AtmosphericDensity` (`KSA/PhysicsEnvironment.cs:21`, `:23`) via `public ref readonly PhysicsEnvironment PhysicsEnvironment => ref _environment;` `KSA/Vehicle.cs:527`. |
| `StructuralLoad.PeakGLoad` | **NEW IN 5168 — VERIFIED, with a big caveat** | `public ref readonly StructuralLoad StructuralLoad => ref _structuralLoad;` `KSA/Vehicle.cs:531`; `public struct StructuralLoad { public double PeakGLoad; public double MaxGLoad; public double PeakDynamicPressure; public double MaxDynamicPressure; public bool IsPressureHydrodynamic; public double GLoadFraction => …; public double DynamicPressureFraction => …; }` `KSA/StructuralLoad.cs:3-18` | as listed | Did **not** exist in 5117. Written at `KSA/VehicleUpdateTask.cs:492-497` (inside `DetectStructuralFailure`, only reached from `ApplyFullPhysics`), applied at `KSA/Vehicle.cs:2344`, reset to `default` each prepared step at `KSA/VehicleUpdateState.cs:287`. **⇒ all-zero for any vehicle not under full physics.** Static helper `public static StructuralLoad? GetControlledStructuralLoad()` `KSA/Vehicle.cs:6097`. |

---

## 2. Harmony patch points

| Row | Status | Exact current declaration | file:line | Note |
|---|---|---|---|---|
| `Universe.DestroyVehicleFromEvent` | **VERIFIED** | `public static void DestroyVehicleFromEvent(Vehicle vehicle, VehicleDestructionEvent destructionEvent)` | `KSA/Universe.cs:1699` | **static, public.** Early-returns `if (vehicle.IsDisposed)`. Vehicle is fully intact at prefix time — reads of speed/pos/`Crew`/mass are valid. Tail-calls `DestroyVehicle(vehicle)` `:1733`. |
| `VehicleDestructionCause` — 6 values | **VERIFIED** | `public enum VehicleDestructionCause { GroundImpact, OceanImpact, Collision, ExcessiveGForce, AerodynamicForces, HydrodynamicForces }` | `KSA/VehicleDestructionCause.cs:3-11` | Byte-identical to 5117. Cause selection logic: `KSA/VehicleUpdateTask.cs:502`. |
| `VehicleDestructionEvent.PeakGLoad` / `PeakDynamicPressure` | **VERIFIED** | `public required VehicleDestructionCause Cause; public required float PeakGLoad; public required float PeakDynamicPressure;` + `public void Apply(Vehicle vehicle)` | `KSA/VehicleDestructionEvent.cs:5-14` | Byte-identical to 5117. `Apply` calls `Universe.DestroyVehicleFromEvent(vehicle, this)`. Both floats. |
| `GroundImpactEvent.Apply(Vehicle)` | **VERIFIED** | `public void Apply(Vehicle vehicle)` on `public class GroundImpactEvent : IVehicleRenderEvent` | `KSA/GroundImpactEvent.cs:21` (class `:5`) | Body only spawns FX (and is `IsImpactFxSuppressed`-gated) — a **postfix still fires for every impact**, suppressed or not. |
| `GroundImpactEvent.ImpactVelocity` | **NEW IN 5168** | `public required float ImpactVelocity;` | `KSA/GroundImpactEvent.cs:9` | Added r5162 ("debris launch speed driven by true impact speed instead of kinetic energy"). **Absent in 5117.** It is the closing **normal** speed (m/s): `num = Vector3.Dot(vector2, double8.ToBepu())` at `KSA/ConstraintSim.cs:726`, assigned `:738`. |
| `GroundImpactEvent.ImpactKineticEnergy` / `IsLaunchPad` | **VERIFIED** | `public required float ImpactKineticEnergy;` `:7`; `public required bool IsLaunchPad;` `:19` | `KSA/GroundImpactEvent.cs` | Also `Parent` (`IParentBody`) `:11`, `Vehicle` `:13`, `ImpactDirCcf` (`double3`) `:15`, `ImpactPosCcf` `:17`. KE = `0.5 * TotalMass * v²` (`KSA/ConstraintSim.cs:729`). `IsLaunchPad` = the struct handle matched `BepuHandles.LaunchPadCollider` (`:742-744`). |
| Ground-impact production (rate limit) | **NEW INFO** | produced in `private void DetectTerrainContact(VehicleUpdateState, CollidableReference, CollidableReference, CollidablePair, float, Vector3, int)` | `KSA/ConstraintSim.cs:705`, event built `:733-745` | **0.5 s per-vehicle debounce**: `if (!((SimStep.NextTime - sourceState.LastGroundImpactTime).Seconds() < 0.5))` `:730`. Only fires for **dynamic vs static** (terrain or launchpad) contacts with a positive closing normal speed. Bounces inside 0.5 s are silently merged. |
| `IsImpactFxSuppressed()` | **VERIFIED** | `public bool IsImpactFxSuppressed()` → `Program.GetPlayerTime() - _lastTeleportTime < 5.0` | `KSA/Vehicle.cs:5271-5274` | Public instance. 5 s post-teleport window as the plan stated. |
| `WaterSplashEvent` | **VERIFIED** | `public class WaterSplashEvent : IVehicleRenderEvent` with `public required float ImpactKineticEnergy;` `:5`, `public required IParentBody Parent;` `:7`, `public required Vehicle Vehicle;` `:9`, `public required double OceanRadius;` `:11`, `public void Apply(Vehicle vehicle)` `:13` | `KSA/WaterSplashEvent.cs` | Byte-identical to 5117. **No `ImpactVelocity`** — the plan's `v ≈ √(2E/m)` reconstruction is still required. Production: `KSA/VehicleUpdateTask.cs:455` `private static void DetectWaterSplash(VehicleUpdateState, Situation, SimTime)` — **1 s debounce** and a **0.5 m/s floor** (`:466`), KE at `:469`. |
| **Impacts apply before destructions in the same frame batch** | **VERIFIED — and stronger than claimed** | `public void ApplyRenderEventsToVehicles()` | `KSA/VehicleUpdateTask.cs:410-453` | See §3. Byte-identical logic to 5117. |
| `Universe.DestroyVehicle` | **VERIFIED** | `public static void DestroyVehicle(Vehicle vehicle)` | `KSA/Universe.cs:1736` | static, public. Calls `vehicle.EndAllCrewMissions()` `:1742`, clears `Program.ControlledVehicle`, clears other vehicles' `Target`, then `vehicle.Dispose()` `:1760`. |
| `Vehicle.Recover()` | **VERIFIED** | `public void Recover()` | `KSA/Vehicle.cs:2765` | Instance, public, no args. Ends crew missions inline (`:2780`), then enqueues `VehicleDestroyData { Vehicle = this, Recovered = true }` `:2788-2792`. |
| `VehicleDestroyData.Recovered` | **VERIFIED** | `public bool Recovered;` on `public struct VehicleDestroyData : IApplicable` | `KSA/InputEvents.cs:491` (struct `:487`, `public void Apply()` `:493`) | The `Recovered` flag is exactly the manual-destroy-vs-recover discriminator, checked at `:513`. |
| `Vehicle.KillCrew` | **VERIFIED** | `public void KillCrew()` | `KSA/Vehicle.cs:2796` | Instance, public, no args. **Exactly one caller** — see §4. |
| `KittenRosterEntryData.Kia` | **VERIFIED** | `public bool Kia;` | `KSA/KittenRosterEntryData.cs:29` | See §4 for the full write graph. |
| `CelestialSystem.Register(Astronomical)` | **VERIFIED** | `public void Register(Astronomical celestial)` — **instance**, public | `KSA/CelestialSystem.cs:79` | Single overload, no vehicle-specific variant (`Vehicle : Astronomical` `KSA/Vehicle.cs:27`; `KittenEva : Vehicle` `KSA/KittenEva.cs:8`). Universal funnel is the `Astronomical` ctor: `System.Register(this);` `KSA/Astronomical.cs:159` (gated on `register` param). Explicit post-hoc registers: save-load `KSA/Vehicle.cs:1293`, EVA save-load `KSA/KittenEva.cs:63`, **rename** `KSA/CelestialSystem.cs:109`. |
| Deregister counterpart | **CHANGED (better hook exists)** | `public void Deregister(Astronomical astronomical)` — instance, public | `KSA/CelestialSystem.cs:84` | The true single removal choke point is `public void Dispose(bool endMission)` `KSA/Vehicle.cs:3510`, which deregisters at `:3520` and covers destroy / dock-consume / EVA-board / shutdown. Also note `CelestialSystem.DestroyAllVehicles` `:119` (called by `Universe.DeserializeSave`) and `Rename` `:107`. |
| `SequenceList.ActivateNextSequence(Vehicle)` | **VERIFIED** | `public void ActivateNextSequence(Vehicle vehicle)` — **instance**, public | `KSA/SequenceList.cs:135` | Single overload. **Only call site in the whole game:** `Parts.SequenceList.ActivateNextSequence(this);` `KSA/Vehicle.cs:3342`, guarded by `action == InputAction.CameraUp && keyAction == GlfwKeyAction.Release` (the stage key). `SetActiveSequence(int)` `:709` does **not** actuate parts. |
| `DockingPort.Dock` | **CHANGED (signature is richer than the plan implies)** | `public Vehicle? Dock(Vehicle thisVehicle, Vehicle otherVehicle, DockingPort otherVehicleDockingPort, out PoseChange consumedToCombined)` — **instance**, public | `KSA/DockingPort.cs:422` | Returns the **surviving combined vehicle** (or `null` if already docked). Delegates to `otherVehicle.MergeFrom(...)` `KSA/Vehicle.cs:1551`. Callers: `KSA/DockingEvent.cs:18` (physics), `KSA/InputEvents.cs:407` (player). |
| `DockingPort.Undock` | **CHANGED (signature)** | `public Vehicle? Undock(Vehicle oldVehicle, out PoseChange combinedToSplit)` — instance, public | `KSA/DockingPort.cs:460` | Body: `return oldVehicle.Split(Connector, PushoffImpulse, out combinedToSplit);`. Caller: `KSA/InputEvents.cs:384`. |
| Dock/undock hook choice | **NEW INFO** | `public class DockingEvent : IVehicleRenderEvent` with `OtherVehicle` `:5`, `OtherDockingPort` `:7`, `DockingPort` `:9`, `public void Apply(Vehicle vehicle)` `:11` | `KSA/DockingEvent.cs` | `DockingEvent.Apply` covers **only physics-initiated** docking (produced `KSA/ConstraintSim.cs:798`, drained `KSA/VehicleUpdateTask.cs:418-422`). Player-commanded dock/undock goes through `InputEvents` instead. **Patch `DockingPort.Dock`/`Undock` directly** to catch both — all call sites are game-thread. Note `DockingEvent` is **suppressed** if a destruction is pending the same frame (`KSA/VehicleUpdateTask.cs:415-416`). |
| `EVADoor.CreateKittenEva` (private?) | **CHANGED — name collision** | `private KittenEva? CreateKittenEva(Vehicle vehicle, IVASeat seat, KittenRosterEntryData rosterEntry)` — **private instance** | `KSA/EVADoor.cs:133` | Only caller `KSA/EVADoor.cs:83` (from `public bool ShowContextMenu(Vehicle vehicle)` `:61`). **The other one:** `public static KittenEva CreateKittenEva(CelestialSystem system, VehicleTemplate template, IParentBody parent, string id)` `KSA/KittenEva.cs:42` (scenario/template path, caller `KSA/VehicleTemplate.cs:104`). See **B1**. |
| `Vehicle.AddCrew` / `AddCrewToFirstAvailableSeat` | **VERIFIED** | `public bool AddCrew(KeyHash kittenHash, string partId, string seatId)` `KSA/Vehicle.cs:713`; `public bool AddCrewToFirstAvailableSeat(KeyHash kittenHash)` `KSA/Vehicle.cs:766` | as listed | Both instance, public, return `bool`. Also `public bool RemoveCrew(KeyHash kittenHash)` `:791`. `kittenHash` = `KeyHash.Make(rosterEntry.Name)` = `KittenRosterEntryData.NameHash` (`:38`). EVA-boarding call site: `KSA/Part.cs:2015-2019`. |
| EVA vehicle `Id` **is** the kitten's roster name | **VERIFIED (with 2 caveats)** | `new KittenEva(Universe.CurrentSystem, rosterEntry.Character, vehicle.Body2Cce, vehicle.BodyRates, vehicle.Parent, rosterEntry.Name, backPackPart, vehicle.Orbit)` | `KSA/EVADoor.cs:142` matched to ctor `KSA/KittenEva.cs:35` (6th positional param is `string id`) | Flows to `Id = id; Hash = KeyHash.Make(id);` `KSA/Astronomical.cs:153-155`. Independently confirmed by the boarding path (`KeyHash kittenHash = KeyHash.Make(vehicle.Id.AsSpan());` `KSA/Part.cs:2015`) and by disposal (`Universe.KittenRoster.Find(Id)?.EndMission();` `KSA/Vehicle.cs:3517`). **Caveats:** (a) `CelestialSystem.Rename(Vehicle, string)` `:100` is not `KittenEva`-aware and would break the invariant; (b) an EVA spawned from a `VehicleTemplate` takes an arbitrary id. |
| `Universe.DeserializeSave` | **VERIFIED** | `public static void DeserializeSave(UniverseData universeData)` | `KSA/Universe.cs:2140` | static, public, single overload. Runs **after** `CurrentSystem.DestroyAllVehicles()` — true teardown+rebuild boundary. Sets `KittenRoster = universeData.KittenRoster` `:2178`. Third call site: `KSA.Networking/NetworkClient.cs:128` (multiplayer join is also a session boundary). |
| `Universe.LoadSystem` | **VERIFIED** | `public static void LoadSystem(string id)` | `KSA/Universe.cs:167` | static, public. Creates a fresh `KittenRosterData` `:176`, calls `AssignStartingCrew()` `:181`, then `OnLoaded()` `:178`. |
| Additional session boundaries | **NEW INFO** | `public static void OnLoaded()` `KSA/Universe.cs:2240`; `public static void LoadDefaultSystem()` `KSA/Universe.cs:158`; `public override void Load()` `KSA/UncompressedSave.cs:45`; `public static void OnGameLoaded()` `KSA/Program.cs:2258` | as listed | **GONE / never existed:** no `LoadSave`, `NewGame`, `Quickload`/`QuickSave` anywhere. `Program.OnGameLoaded` is the cleanest "load finished" postfix. |
| `Vehicle.Teleport` | **VERIFIED** | `public void Teleport(Orbit? orbit, doubleQuat? body2Cce, double3? bodyRates)` | `KSA/Vehicle.cs:2031` | Instance, public. All three params nullable. This is the real mutation point. |
| `Vehicle.TeleportToLocation` | **CHANGED (does not teleport)** | `public void TeleportToLocation(Celestial celestial, double lat, double lon)` | `KSA/Vehicle.cs:3914` | Enqueues `InputEvents.TeleportInputData` at `:3927`; the actual teleport happens in `TeleportInputData.Apply()` `KSA/InputEvents.cs:295`. See **B7**. Companion `private void TeleportToLocationById(Celestial, string)` `:3930`. |
| `Vehicle.RefillConsumables` | **VERIFIED** | `public void RefillConsumables()` | `KSA/Vehicle.cs:2981` | Instance, public, no args. Companion `public void DepleteConsumables()` `:2988` — patch it too (it is equally an "assist" marker in the other direction). |
| `InputEvents.VehicleResourcesChangeData` | **VERIFIED** | `public struct VehicleResourcesChangeData : IApplicable` with `public Vehicle Vehicle;` `:534`, `public bool Refill;` `:536`, `public bool Empty;` `:538`, `public bool Control;` `:540`, `public void Apply()` `:542` | `KSA/InputEvents.cs:532` | Buffer `public static TypedBuffer<VehicleResourcesChangeData> VehicleResourcesChangeBuffer` `:838`, drained in `public static void ApplyInputEvents()` `:932` at `:946`. Producers: `KSA/Universe.cs:946` (`control`), `:1154` (`refill`), `:1185` (`empty`) — all terminal commands. |
| **Never patch worker-thread detectors** | **VERIFIED — and one moved file** | `private void DetectTerrainContact(VehicleUpdateState, CollidableReference, CollidableReference, CollidablePair, float, Vector3, int)` **`KSA/ConstraintSim.cs:705`**; `private void DetectDockingEvent(VehicleUpdateState, VehicleUpdateState, CollidableReference, CollidableReference, int, int)` **`KSA/ConstraintSim.cs:751`**; `private static void DetectStructuralFailure(VehicleUpdateState)` `KSA/VehicleUpdateTask.cs:481`; `private static void DetectWaterSplash(VehicleUpdateState, Situation, SimTime)` `KSA/VehicleUpdateTask.cs:455` | as listed | Still true. All four are reached from `VehicleUpdateTask.Run()` `KSA/VehicleUpdateTask.cs:541` → `DoWorkAndStageResults()` `:221` → `ApplyFullPhysics()` `:792`, and `VehicleUpdateTask : IJob` `:14` is queued on `JobSystems.VehicleSolvers`. **Note the plan implies all three live together; `DetectTerrainContact`/`DetectDockingEvent` are in `ConstraintSim.cs`, not `VehicleUpdateTask.cs`.** Game-thread apply-side counterparts are in the table below. |

### Worker-thread detector → game-thread apply-side counterpart

| Worker-thread detector (DO NOT PATCH) | Game-thread counterpart (PATCH THIS) |
|---|---|
| `ConstraintSim.DetectTerrainContact` `KSA/ConstraintSim.cs:705` | `GroundImpactEvent.Apply(Vehicle)` `KSA/GroundImpactEvent.cs:21` |
| `VehicleUpdateTask.DetectWaterSplash` `KSA/VehicleUpdateTask.cs:455` | `WaterSplashEvent.Apply(Vehicle)` `KSA/WaterSplashEvent.cs:13` |
| `VehicleUpdateTask.DetectStructuralFailure` `KSA/VehicleUpdateTask.cs:481` | `VehicleDestructionEvent.Apply(Vehicle)` `KSA/VehicleDestructionEvent.cs:11` → `Universe.DestroyVehicleFromEvent` `KSA/Universe.cs:1699` |
| `ConstraintSim.DetectDockingEvent` `KSA/ConstraintSim.cs:751` | `DockingEvent.Apply(Vehicle)` `KSA/DockingEvent.cs:11` — but prefer `DockingPort.Dock`/`Undock` (covers player-initiated too) |
| — (batch boundary) | `Universe.ApplyVehicleSolvers()` `KSA/Universe.cs:1653` — public static, called from `Program.PrepareFrame` `KSA/Program.cs:1912` |

---

## 3. The ordering claim — **VERIFIED, and it is stronger than the plan states**

`KSA/VehicleUpdateTask.cs:410-453`, byte-identical to build 5117:

```csharp
public void ApplyRenderEventsToVehicles()
{
    for (int i = 0; i < _vehicleStates.Count; i++)          // ── PASS 1
    {
        VehicleUpdateState vehicleUpdateState = _vehicleStates[i];
        bool flag = vehicleUpdateState.DestructionEvent != null;
        if (!flag)
        {
            DockingEvent dockingEvent = vehicleUpdateState.DockingEvent;
            if (dockingEvent != null) { dockingEvent.Apply(...); vehicleUpdateState.DockingEvent = null; }
        }
        GroundImpactEvent groundImpactEvent = vehicleUpdateState.GroundImpactEvent;
        if (groundImpactEvent != null) { groundImpactEvent.Apply(...); vehicleUpdateState.GroundImpactEvent = null; }
        WaterSplashEvent waterSplashEvent = vehicleUpdateState.WaterSplashEvent;
        if (waterSplashEvent != null) { waterSplashEvent.Apply(...); vehicleUpdateState.WaterSplashEvent = null; }
        if (flag) { _pendingDestruction.Add(vehicleUpdateState); }   // deferred
    }
    for (int j = 0; j < _pendingDestruction.Count; j++)     // ── PASS 2
    {
        VehicleUpdateState vehicleUpdateState2 = _pendingDestruction[j];
        VehicleDestructionEvent destructionEvent = vehicleUpdateState2.DestructionEvent;
        if (destructionEvent != null) { destructionEvent.Apply(...); vehicleUpdateState2.DestructionEvent = null; }
    }
    _pendingDestruction.Clear();
}
```

This is a **two-pass** structure, not merely intra-iteration ordering: **every** impact/splash across the whole task's vehicle list is applied before **any** destruction in that task. Destructions are collected into `_pendingDestruction` and drained afterwards.

Frame-level context (`KSA/Program.cs:1900` `private OnFrameResult PrepareFrame(double, double)`):

```
1910  JobSystems.VehicleSolvers.Wait();          // workers finished
1912  Universe.ApplyVehicleSolvers();            // → per-task: ApplyResultsToVehicles, then ApplyRenderEventsToVehicles
1918  InputEvents.ApplyInputEvents();            // → VehicleDestroyBuffer (manual destroy / recover / KillCrew), teleports, refills
1946  Universe.ExecuteNextVehicleSolvers(...);   // queue next worker batch
```

`Universe.ApplyVehicleSolvers()` (`KSA/Universe.cs:1653`, public static) loops all tasks and calls `ApplyRenderEventsToVehicles()` per task (`:1687-1693`). A vehicle belongs to exactly one task, so **for a given vehicle Id the impact-before-destruction guarantee is absolute.**

**Implementation guidance for the "survived the lithobrake" rule:**
- Record impacts in a postfix on `GroundImpactEvent.Apply` / `WaterSplashEvent.Apply`.
- Record deaths in a prefix on `Universe.DestroyVehicleFromEvent`.
- **Resolve at the batch boundary**: postfix `Universe.ApplyVehicleSolvers` (public static, game thread) — every impact and every physics destruction for the frame has landed by then. Do **not** resolve in `[StarMapBeforeGui]` without accounting for `InputEvents.ApplyInputEvents()` at `Program.cs:1918`, which can manually destroy the same vehicle a few lines later in the same frame.
- Reject records when `vehicle.IsImpactFxSuppressed()` (5 s post-teleport) or `evt.IsLaunchPad`, as the plan says.
- Additional caveats the plan does not mention: the impact detector has a **0.5 s per-vehicle debounce** (`KSA/ConstraintSim.cs:730`) and the splash detector a **1 s debounce + 0.5 m/s floor** (`KSA/VehicleUpdateTask.cs:466`) — a bouncing vehicle reports one impact per 0.5 s, not per bounce.

---

## 4. Crew survival — **DEFINITIVE ANSWER**

**The plan's claim is CORRECT and unchanged in build 5168: the physics RUD path does NOT kill crew. Only a player-initiated manual destroy does.**

### The two disjoint paths

**Path A — physics destruction (RUD). No KIA.**

```
DetectStructuralFailure                     KSA/VehicleUpdateTask.cs:481   (worker thread)
  → vehicleState.DestructionEvent = new VehicleDestructionEvent{...}       :503
VehicleDestructionEvent.Apply(vehicle)      KSA/VehicleDestructionEvent.cs:11
  → Universe.DestroyVehicleFromEvent(v, e)  KSA/Universe.cs:1699
      → Universe.DestroyVehicle(vehicle)    KSA/Universe.cs:1733 → :1736
          → vehicle.EndAllCrewMissions()    KSA/Universe.cs:1742
              → KittenRosterEntryData.EndMission()   KSA/Vehicle.cs:821
          → vehicle.Dispose()               KSA/Universe.cs:1760
```

`EndAllCrewMissions()` (`KSA/Vehicle.cs:806-830`) calls `EndMission()` (banks mission time, clears `AssignedVehicleId`) or, pre-launch, just clears `AssignedVehicleId`. **`Kill()` is never reached. `Kia` stays `false`.** Kittens are freed back into the available pool.

**Path B — manual destroy. KIA.**

```
InputEvents.VehicleDestroyData.Apply()      KSA/InputEvents.cs:493
  if (!Recovered)  → Vehicle.KillCrew()     KSA/InputEvents.cs:513-516
      → KittenRosterEntryData.Kill(HasLaunched)   KSA/Vehicle.cs:2804 (seats) and :2809 (KittenEva, keyed by Vehicle.Id)
          → Kia = true                      KSA/KittenRosterEntryData.cs:108
  → Universe.DestroyVehicle(Vehicle)        KSA/InputEvents.cs:528
```

### Evidence: the complete call graph

- **`Kia = true` appears exactly once** in the entire decomp: `KSA/KittenRosterEntryData.cs:108`, inside `public void Kill(bool hasLaunched)` `:96` (idempotent, guarded by `if (!Kia)`). **`Kia` is never reset to `false` anywhere.**
- **`Kill()` has exactly two call sites**, both inside `Vehicle.KillCrew()`: `KSA/Vehicle.cs:2804` and `KSA/Vehicle.cs:2809`.
- **`KillCrew()` has exactly one caller**: `KSA/InputEvents.cs:515`, guarded by `if (!Recovered)` at `:513`.
- **`VehicleDestroyBuffer` has exactly four producers:**

| Producer | file:line | `Recovered` | KIA? |
|---|---|---|---|
| `Vehicle.Recover()` | `KSA/Vehicle.cs:2788-2792` | `true` | **No** |
| `AbandonPopup` "CONFIRM" (ESC menu → Abandon) | `KSA/AbandonPopup.cs:14-18` | `false` (default) | **Yes** |
| `Universe.Destroy(string id)` terminal command | `KSA/Universe.cs:1126-1130` | `false` | **Yes** |
| Universe-manifest window "X" delete button | `KSA/UniverseManifest.cs:372-375` | `false` | **Yes** |

**Nothing in the physics/destruction path touches `VehicleDestroyBuffer` at all.** `Universe.DestroyVehicleFromEvent` → `Universe.DestroyVehicle` bypasses `InputEvents` entirely.

**Conclusion:** a kitten in KSA 5168 dies **only** when the player explicitly abandons/deletes the vehicle from the ESC menu, the Universe manifest, or the console `destroy` command. A vehicle that explodes from ground impact, ocean impact, collision, excessive g-force, aerodynamic or hydrodynamic forces returns its crew to the roster alive, with mission time banked and `MissionCount` already incremented.

### Practical consequence for catlog

`Vehicle.KillCrew` is a **player-intent** signal, not a fatality signal. catlog patches it for two things: the intent timestamp that labels the resulting `kitten.kia` as `manual_destroy`, and — since 2026-08-09 — the **crew read that attributes the death to a flight** (MOD-073). It is the last point at which the seats are readable and the flight is still open: `Vehicle.Dispose` follows in the same frame, and the roster diff that notices the death a tick later has only a name. But **never treat a `VehicleDestructionEvent` as a crew fatality**. If catlog wants a "crew survived the RUD" statistic, the game gives it for free — everyone always survives. If catlog wants a *real* fatality metric it must define one itself (e.g. "crewed vehicle destroyed by `VehicleDestructionCause.GroundImpact`"), and label it as a catlog-defined metric rather than a game-reported KIA. Roster-diffing `Kia` will only ever fire on manual destroys.

### Does the 2.5× kitten g-load change affect this?

**No — it makes kittens harder to destroy, not harder to kill.** New in 5168 (`KSA/VehicleUpdateTask.cs:486-490`, changelog r5144):

```csharp
double num = VehicleStructuralLimits.EffectiveMaxGLoad(readOnlyProps.ComputeBoundingSphereRadiusAsmb());
if (vehicleState.IsKitten) { num *= 2.5; }
```

with `public bool IsKitten;` `KSA/VehicleUpdateState.cs:80`, set `IsKitten = ReadOnlyVehicle is KittenEva;` `:258`, and `public static double EffectiveMaxGLoad(double boundingSphereRadius) => Math.Max(5.0, 50.0 * Math.Min(1.0, 5.0 / Math.Max(boundingSphereRadius, 0.001)));` `KSA/VehicleStructuralLimits.cs:17-20`. Since a kitten's bounding sphere is tiny, `EffectiveMaxGLoad` already saturates at 50 g; ×2.5 ⇒ **125 g** for an EVA kitten. So an EVA kitten now survives far harder impacts as a *vehicle*. It changes the rate at which EVA `KittenEva` vehicles are destroyed, not whether destruction implies KIA. (When a `KittenEva` **is** destroyed by physics, the same Path A applies — `EndAllCrewMissions` finds no seats, and the `this is KittenEva` roster-`Kill` branch at `KSA/Vehicle.cs:2809` lives in `KillCrew`, which physics never calls. So an EVA kitten smashed into a mountain at 200 g is also not KIA.)

---

## 5. New in 5168 that is relevant to telemetry / leaderboards

Items the older plan could not have covered.

| Item | Where | Why it matters |
|---|---|---|
| **`StructuralLoad` struct + `Vehicle.StructuralLoad`** — brand new file | `KSA/StructuralLoad.cs`, `KSA/Vehicle.cs:531` | Peak g and peak dynamic pressure are now **pollable in real time**, not just at death. Also gives `MaxGLoad`/`MaxDynamicPressure` (the per-vehicle limits) and `GLoadFraction`/`DynamicPressureFraction`. This turns "closest call" / "highest sustained g" into a first-class leaderboard category with no Harmony patch at all. **Caveat B10: zero when not under full physics.** |
| **G-force / pressure warning alerts** (changelog r5165) | `KSA/LoadAlert.cs` (new file), thresholds `WARN_FRACTION = 0.7`, `WARN_RELEASE = 0.65`, `DANGER_FRACTION = 0.85`, `DANGER_RELEASE = 0.8` at `:15-21`; reads `Vehicle.GetControlledStructuralLoad()` `:54` | Gives catlog ready-made, game-blessed thresholds with hysteresis for a "near-death experience" event. Copy the fractions rather than inventing your own. |
| **Kitten locomotion subsystem** (changelog r5128/r5130/r5131/r5134/r5142) — 6 new files | `KSA/KittenLocomotion.cs`, `LocomotionMode.cs`, `LocomotionState.cs`, `LocomotionFacts.cs`, `LocomotionCommand.cs`, `KittenLocomotionTuning.cs` | The whole tumble-detection surface is one build old. Beyond `Mode`, `LocomotionState` exposes `ModeStartTime`, `GroundSpeed`, `JumpFired`, `LiftoffTime`, `LastContactTrueTime` — enough for "longest jump", "time airborne", "distance walked vs MMU'd" without any patching. `LocomotionState` is a value copy off a public property — no reflection. |
| **`LocomotionMode.Rightening`** | `KSA/LocomotionMode.cs:8-9`, transition `KSA/KittenLocomotion.cs:55-61` | A tumble now ends `Tumbling → Rightening → Grounded` (after `SettleTime = 1 s` below `SettleSpeedThreshold = 0.2 m/s`). Counting transitions **into** `Tumbling` is still correct; counting transitions **out of** `Tumbling` would double-count via `Rightening`. |
| **`LocomotionMode.Ladder`** (changelog r5161) | `KSA/LocomotionMode.cs:10` | Declared but **not functional yet** ("SoonTM"). Present in `DeriveMode` (`KSA/KittenLocomotion.cs:26-29`) and `StepLadder` (`:292`) but nothing sets it. Do not build a ladder metric yet; do handle the enum value so a future build does not crash a switch. |
| **`KittenTuningWindow`** — live tuning of the tumble gate | `KSA/KittenTuningWindow.cs:9`, instantiated `KSA/Program.cs:3422` | **Integrity hole.** See **B9**. Also exposes `JumpDeltaV`, `RunSpeed`, `WalkSpeed` etc. — a kitten-distance or jump leaderboard is forgeable via this shipped window. Snapshot the whole `KittenLocomotionTuning.Current` (or a hash of it) alongside any kitten-locomotion record. |
| **`GroundImpactEvent.ImpactVelocity`** (changelog r5162) | `KSA/GroundImpactEvent.cs:9` | New this build. This is exactly the field the plan's "survived lithobrake at N m/s" record needs, and it did **not** exist in 5117 — so the record type only became possible now. It is the closing **normal** speed, not total speed. |
| **Orbit-decay-to-impact crash fix** (changelog r5121) | `KSA/VehicleUpdateTask.cs:1405-1438` (new `flag` / `PatchTransition.Impact` branch) vs `_prev/.../VehicleUpdateTask.cs:1216` (which threw) | Previously, a patch ending in `Impact` with no next patch **threw an exception inside `VehicleUpdateTask.Run()`** (swallowed and logged at `:552-570`), so decaying-orbit impacts could be silently lost. Now the vehicle is taken off rails (`newStates.Props.SetOnRails(isOnRails: false)`) and gets full physics. **⇒ ground-impact and RUD detection for orbital-decay impacts is materially more reliable in 5168 than in any earlier build.** Do not carry forward any workaround for missing decay impacts. |
| **Crew-assignment changes** (changelog r5129/r5163) | `Universe.AssignStartingCrew()` `KSA/Universe.cs:181`, `Universe.SeatCrew(string, int, Queue<…>)` `:203` | New-game start now auto-seats crew into `"Gemini7"` and `"Rocket"` and calls `MarkLaunched()` on them, plus `StartMission("EVA")` for any pre-existing `KittenEva`. This means `MissionCount` is already `>0` and `HasLaunched` already `true` for the starting vehicles at t=0 — a naive "first launch" detector will misfire on a fresh save. Also new "Fill Seats"/"Fill ALL vehicle seats" UI buttons mean crew composition can change between samples without a launch. |
| **Control-point relocation** ("Control From Here", changelog r5133) | `KSA/Vehicle.cs:563-571` — `public Part? TargetPart { get; private set; }`, `public Part? ControlPart`, `public Part.Connector? ControlConnector`, `public doubleQuat Ctrl2Body => …`, `public double3 CtrlOriginBody => …`, `public double3 CtrlRates => …` `:587` | New this build. Attitude/navball telemetry is now expressed in a **control frame** that the player can move at will (`Rocket.UpdateThrusterCache` and `ThrusterController.RecomputeDynamicData` both gained a `ctrl2Body` parameter — see below). Any attitude-based metric must record which control point was active, or normalize back to the body frame. |
| **Breaking signature changes in the RCS/engine layer** (5117→5168) | `public static void UpdateThrusterCache(ReadOnlyPhysicsStates, doubleQuat ctrl2Body, …)` `KSA/Rocket.cs:187` (new 2nd param); `ThrusterController.RecomputeDynamicData(…, floatQuat ctrl2Body)` `KSA/ThrusterController.cs:85`; `public bool TrySampleThrustCurve(ThrustCurveSamples samples, out ThrustCurvePreview preview)` `KSA/SolidMotor.cs:315` (was `Span<float>`), new `public ref struct ThrustCurveSamples` `KSA/SolidMotor.cs:23` | Not detection surfaces catlog needs, but they are the *shape* of what breaks between builds — worth `[KsaAnchor]`-ing if catlog ever reads thrust curves or RCS maps. Nozzle/engine read surface itself (`RocketNozzleState`, `EngineController`, `RocketCoreState`, all templates) is **byte-identical** to 5117. |
| **SOI transition handling in physics** | `if (newStates.CheckSoiTransitions()) { PopulateAnalyticStatesFromKinematicStates(vehicleState); }` `KSA/VehicleUpdateTask.cs:1685-1688` (new) | SOI changes are now resolved inside the physics step for off-rails vehicles. The plan's polled `vehicle.Parent.Id` diff still works, but expect SOI transitions to be observable a step earlier and for off-rails vehicles that previously would not have transitioned mid-bubble. |
| **RCS thrust rebalance** (changelog r5119, r5128) | — | "Reduced RCS thrust overall", "small RCS thrusters noticeably less thrust", "Greatly increased the thrust of Kitten RCS". Purely numeric, but any **historical** delta-v or maneuver-efficiency leaderboard is not comparable across the 5117/5168 boundary. Stamp the game build on every batch. |

---

## 5b. Career identity and the career clock — **re-verified 2026-08-07 against 2026.8.5.5168**

The question: catlog wants "fastest time from game start to first orbit". That needs two things — a
clock that counts from the start of the save, and a way to tell one save from another.

### 5b.1 The clock is career-relative and it is in the save — **VERIFIED**

| Claim | Evidence |
|---|---|
| `Universe.GetElapsedSimTime()` returns `_lastSimStep.NextTime` | `KSA/Universe.cs:2108-2112`, field at `:42` |
| `SimTime` is a `readonly struct` over **one `double` of seconds** | `KSA/SimTime.cs:6-8`; accessor `Seconds()` `:67-70` |
| A new game starts at exactly **0.0** | `_lastSimStep` is never initialised at boot — the static ctor `KSA/Universe.cs:2337-2351` does not touch it, and `Universe.LoadSystem` `:167-179` does not either, so it is `default(SimStep)` ⇒ `SimTime` 0.0 |
| Elapsed time is **written to the save** | `UniverseData.Create()` sets `GameTime = new SimTimeReference(Universe.GetElapsedSimTime())` `KSA/UniverseData.cs:43`; field `:10-11`; written to `universe.xml` `:55-58` |
| …and **restored on load** | `Universe.DeserializeSave` builds `new SimTime(universeData.GetElapsedSeconds())` and assigns `_lastSimStep`/`_nextSimStep` `KSA/Universe.cs:2160-2167`; getter `KSA/UniverseData.cs:108-111` |
| There is **no calendar epoch in code** | No `StartDate`/`Epoch`/`J2000` identifier exists. The ephemerides are anchored at JD 2461009.5 (2025-11-30T00:00Z) but only as an XML comment, `Content/Core/Astronomicals.xml:12` |

**Consequence for catlog:** `sim_t` already *is* the career clock. `docs/events.md` needs no `game_t`
and no per-career origin — only a way to say which career a `sim_t` belongs to.

Two caveats worth knowing:

- **Save round-trip quantisation.** `SimTimeReference : TimeSpanReference` decomposes the value into
  seven `[XmlAttribute]` doubles (`KSA/TimeSpanReference.cs:11-30`, `Populate()` `:51-74`,
  `GetTotalSeconds()` `:417-455`), and `NaNFilteringXmlWriter` rounds every attribute to **4 decimal
  places** (`KSA/NaNFilteringXmlWriter.cs:12-14, 82-88`). Elapsed time therefore round-trips with
  ±5e-5 s of error — irrelevant to a leaderboard in seconds, but it is not bit-exact.
- **A missing `<GameTime/>` loads as 0 with no error.** `TimeSpanReference._value` is only assigned
  when `GetTotalSeconds()` is non-NaN (`:53-57`) and `IsValid()` still returns true (`:99-102`).

### 5b.2 There is no career, save or player identifier — **CONFIRMED, the plan's claim holds**

`the original proposals (now removed)` §1.4 says "KSA has no player/account/save GUID anywhere (verified)"
against a build that is not on disk. Re-verified here against 5168, and it is correct.

The save root is `UniverseData` and it has **exactly four fields** — every one of them enumerated:

| # | Field | Line |
|---|---|---|
| 1 | `GameTime` (`SimTimeReference`) | `KSA/UniverseData.cs:10-11` |
| 2 | `Camera` (`CameraData`) | `KSA/UniverseData.cs:13-14` |
| 3 | `CelestialSystems` (`List<CelestialSystemData>`) | `KSA/UniverseData.cs:16-17` |
| 4 | `KittenRoster` (`KittenRosterData`) | `KSA/UniverseData.cs:19-20` |

No id, no GUID, no creation timestamp, no seed, no save name, no version. Negative results:
`rg "Guid" KSA/` finds only four `OnCursorEnter(Guid, bool)` input callbacks; `rg -i "career|campaign"`
finds **zero**; `rg "Seed"` finds only terrain-noise seeds from templates — the system is a
hand-authored XML template (`Content/Core/Astronomicals.xml`), not generated, so there is no universe
seed to hash.

Everything that is *nearly* an anchor, and why each fails:

| Candidate | Location | Why it fails |
|---|---|---|
| `SaveMetaData.Created` | `KSA/SaveMetaData.cs:16-17` | **The trap.** An overwrite is `Delete(); Make(Id);` (`KSA/UncompressedSave.cs:85-89`) and `Make` constructs a fresh `SaveMetaData` whose field initialiser re-stamps `DateTime.UtcNow`. It is a "last written" time wearing a "created" label. |
| `CelestialSystemData.Id` | `KSA/CelestialSystemData.cs:8-9` | The system template name (`"Sol"`). Constant per install, and **never read back** — `CelestialSystem.DeserializeSave` `KSA/CelestialSystem.cs:612-754` ignores it. |
| Procedural kitten names | `KSA/KittenRosterData.cs:29-47` | 17 of the 20 starting kittens are named from `Random.Shared` with no stored seed, so the roster is an accidental fingerprint — but it mutates as kittens are created (`:49-75`) and die. |
| `GameTime` itself | `KSA/UniverseData.cs:10-11` | Monotone within a career, and exactly the thing you are trying to disambiguate. |
| `SaveMetaData.Version` | `KSA/SaveMetaData.cs:22-23` | The game build string. |

### 5b.3 The save's **name** is the only stable per-save string, and it is reachable

`GameSave.Id` (`KSA/GameSave.cs:13`) is the folder under
`Documents/My Games/Kitten Space Agency/saves` (`KSA/UncompressedSave.cs:19`,
`KSA/GameSaves.cs:105,117`, `KSA/Constants.cs:264`). It is user-typed and unvalidated except for
empty (`KSA/GameSaves.cs:42,229-242`).

It is reachable in exactly two places, and **not** at the boundary catlog already patches:

| Method | Signature | Carries the save? |
|---|---|---|
| `UncompressedSave.Load()` | `KSA/UncompressedSave.cs:45` — instance, **zero args** | **Yes, via `__instance`**: `Id`, `Directory`, `MetaData`, `UniverseData` |
| `UncompressedSave.Make(string)` | `KSA/UncompressedSave.cs:104` — static, returns `GameSave` | **Yes**, via the argument or `__result`. Every write path lands here: terminal `save <name>` `KSA/GameSaves.cs:252-259`, the UI popup `:229-242`, and `Overwrite()` `KSA/UncompressedSave.cs:85-89` |
| `Universe.DeserializeSave(UniverseData)` | `KSA/Universe.cs:2140` | **No.** No name, no path, no stream — only the four-field blob |
| `Program.OnGameLoaded()` | `KSA/Program.cs:2258-2265` | **No.** Body just closes menus |
| `GameSaves.Selected` | `KSA/GameSaves.cs:125` | **Do not use.** It tracks *UI selection*, is unset after a terminal `load`, and `GameSaves.Refresh()` `:211-221` rebuilds every `UncompressedSave` so it can dangle |

**Ordering, and why catlog patches `Load` with a prefix:** `Load()` calls `Universe.DeserializeSave`
itself (`KSA/UncompressedSave.cs:57`), so catlog's existing `SessionBoundaryPostfix` on
`DeserializeSave` fires *inside* `Load`. The career has to be adopted in a **prefix** on `Load`, or
the first session after every load is stamped with the previous career's id.

**Timing hazard, for anyone tempted by `UniverseData.LoadFrom`:** every save in the folder is fully
XML-deserialised at application start (`UncompressedSave` ctor `:23-30` ← `FromDirectory` `:139-173`
← `GameSaves.Refresh()` `KSA/GameSaves.cs:211-221` ← `OnApplicationStart()` `:205-209` ←
`KSA/Program.cs:702`). Patching `LoadFrom` fires N times at boot, not once at load.

**Also:** `Universe.DeserializeSave` assigns `KittenRoster = universeData.KittenRoster`
(`KSA/Universe.cs:2178`) — the live roster becomes the object the cached `UncompressedSave` holds,
and `UniverseData.Create()` assigns it back (`KSA/UniverseData.cs:51`). In-memory save data mutates
as you play, so loading the same slot twice in one session may not give you the original roster.

### 5b.4 Can the mod tell "a different save" from "an earlier point in the same save"?

**Yes, but only by name.** With the two patches above, a load of `apollo` is always the same career
id, so an `apollo` load whose clock is below what catlog has already seen in that career is a rewind
of `apollo`. A load of `gemini` is a different career and is not compared to it at all.

What it cannot tell apart: two saves that share a name after `Delete` + `Make`, and a save folder
copied under a new name. Both are stated as limitations in `docs/events.md` rather than worked
around, because working around them would mean writing catlog state into the player's save
directory.

## 6. Unverifiable / open

| Item | Status | Why |
|---|---|---|
| Everything the plan attributes to build `2026.7.3.4826` | **UNVERIFIABLE** | That decomp tree is not on disk. The only prior tree is `2026.8.3.5117`. Several plan claims (`StructuralLoad.PeakGLoad`, `GroundImpactEvent.ImpactVelocity`, `LocomotionMode.Tumbling`, `TumbleSpeedGate`) are **absent from 5117**, so they cannot have been verified against 4826 either — treat them as newly confirmed here, not re-confirmed. |
| `LocomotionMode.Ladder` reachability | **UNVERIFIABLE from source** | Declared and handled but no producer; changelog says not functional. Needs in-game confirmation once ladders ship. |
| Runtime field-name fidelity | **UNVERIFIABLE from source** | Standing KSA-skill caveat: the shipped binary can differ from the decomp. Resolve every patch target with `AccessTools` + null check + patch-time logging, and dead-latch on failure. |
