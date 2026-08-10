using System;
using System.Collections.Generic;
using System.Reflection;
using HarmonyLib;
using KSA;
using MeowSci.Catlog.Lib.Events;
using MeowSci.Catlog.Lib.Util;

namespace MeowSci.Catlog;

/// <summary>
/// The Harmony patch table. Every target below is transcribed from
/// <c>docs/ksa-integration.md</c> — the decomp-verified reference for build <b>2026.8.5.5168</b> —
/// and each patch carries the table row it came from in a comment at the patch site, so the
/// coupling is legible without opening the doc.
/// </summary>
/// <remarks>
/// <para>
/// <b>Never patch the worker-thread detectors.</b> <c>ConstraintSim.DetectTerrainContact</c>
/// (<c>ConstraintSim.cs:705</c>), <c>ConstraintSim.DetectDockingEvent</c>
/// (<c>ConstraintSim.cs:751</c>), <c>VehicleUpdateTask.DetectStructuralFailure</c>
/// (<c>VehicleUpdateTask.cs:481</c>) and <c>VehicleUpdateTask.DetectWaterSplash</c>
/// (<c>VehicleUpdateTask.cs:455</c>) all run on <c>JobSystems.VehicleSolvers</c> worker threads.
/// A patch body there would read half-updated vehicle state and would race the game thread. Every
/// row below is the game-thread <i>apply</i>-side counterpart from the mapping table in
/// <c>docs/ksa-integration.md</c> §2.
/// </para>
/// <para>
/// <b>Resolution is defensive.</b> Every target is resolved with <see cref="AccessTools"/>, null
/// checked, and logged at patch time — decomp drift is real and the shipping binary can differ from
/// the decompiled sources. A target that does not resolve is recorded in
/// <see cref="Unresolved"/>, surfaced in the status window, and simply not patched: the mod loses
/// that one signal and keeps everything else. Nothing here throws into the loader.
/// </para>
/// <para>
/// <b>Every patch body is wrapped.</b> These run inside the game's hottest paths; an escaping
/// exception would destabilize the frame loop and, worse, could abort a game operation mid-way.
/// Each body is <c>try/catch</c>-swallowed with a log-once latch.
/// </para>
/// </remarks>
public static class Patcher
{
    private const string HarmonyId = "meowsci.catlog";

    private static readonly List<Installed> Applied = [];
    private static readonly List<string> UnresolvedTargets = [];
    private static readonly HashSet<string> Recovering = new(StringComparer.Ordinal);
    private static readonly HashSet<string> Destroying = new(StringComparer.Ordinal);
    private static readonly List<GameSignal> Scratch = [];

    private static Harmony? _harmony;
    private static CatlogRuntime? _runtime;
    private static bool _bodyErrorLogged;

    /// <summary>Patch targets that could not be resolved against the running binary.</summary>
    public static IReadOnlyList<string> Unresolved => UnresolvedTargets;

    /// <summary>How many patches were successfully installed.</summary>
    public static int InstalledCount => Applied.Count;

    /// <summary>
    /// Applies every patch. Never throws.
    /// </summary>
    /// <param name="runtime">The runtime the patch bodies raise signals into.</param>
    public static void Patch(CatlogRuntime runtime)
    {
        _runtime = runtime;
        try
        {
            _harmony = new Harmony(HarmonyId);
            InstallAll(_harmony);
            ModLog.Log.Info(
                $"catlog: {Applied.Count} Harmony patches applied"
                + (UnresolvedTargets.Count == 0
                    ? "."
                    : $", {UnresolvedTargets.Count} target(s) unresolved: {string.Join(", ", UnresolvedTargets)}."));
        }
        catch (Exception ex)
        {
            ModLog.Log.Error($"catlog: applying Harmony patches failed: {ex.Message}", ex);
        }
    }

    /// <summary>Removes every patch this class installed. Never throws.</summary>
    public static void Unload()
    {
        try
        {
            if (_harmony is { } harmony)
            {
                // Targeted unpatch by the stored MethodInfo pairs, so an UnpatchAll can never reach
                // a patch some other mod installed under a shared id.
                foreach (Installed installed in Applied)
                    harmony.Unpatch(installed.Original, installed.Patch);
            }
        }
        catch (Exception ex)
        {
            ModLog.Log.Error($"catlog: removing Harmony patches failed: {ex.Message}", ex);
        }
        finally
        {
            Applied.Clear();
            UnresolvedTargets.Clear();
            Recovering.Clear();
            Destroying.Clear();
            _harmony = null;
            _runtime = null;
            ModLog.Log.Info("catlog: Harmony patches removed.");
        }
    }

    private static void InstallAll(Harmony harmony)
    {
        // Universe.CurrentSystem is assigned only after CelestialSystem construction returns, so
        // the system survey must be a postfix. LoadSystem runs once per launch; save loads reuse
        // the immutable cache rather than re-enumerating a swap-removed collection.
        Install(harmony, "Universe.LoadSystem(string)",
            AccessTools.Method(typeof(Universe), nameof(Universe.LoadSystem), [typeof(string)]),
            postfix: nameof(LoadSystemPostfix));

        // ── docs/ksa-integration.md §2, "batch boundary" row ────────────────────────────────
        // | — (batch boundary) | Universe.ApplyVehicleSolvers() KSA/Universe.cs:1653 — public
        // | static, called from Program.PrepareFrame KSA/Program.cs:1912 |
        // Every impact and every physics destruction for the frame has landed by the time this
        // returns. catlog does not close its frame here (that happens in [StarMapBeforeGui], which
        // runs after InputEvents.ApplyInputEvents at Program.cs:1918 and therefore also covers
        // manual destroys); the postfix exists as the heartbeat that proves the solver batch is
        // reaching us at all, which is the first thing to check when nothing is being recorded.
        Install(harmony, "Universe.ApplyVehicleSolvers",
            AccessTools.Method(typeof(Universe), nameof(Universe.ApplyVehicleSolvers)),
            postfix: nameof(ApplyVehicleSolversPostfix));

        // ── §2 row 1 ────────────────────────────────────────────────────────────────────────
        // | Universe.DestroyVehicleFromEvent | VERIFIED | public static void
        // | DestroyVehicleFromEvent(Vehicle, VehicleDestructionEvent) | KSA/Universe.cs:1699 |
        // | Vehicle is fully intact at prefix time — reads of speed/pos/Crew/mass are valid. |
        // This is the physics RUD path (§4 Path A): it never kills crew.
        Install(harmony, "Universe.DestroyVehicleFromEvent",
            AccessTools.Method(typeof(Universe), nameof(Universe.DestroyVehicleFromEvent)),
            prefix: nameof(DestroyVehicleFromEventPrefix));

        // ── §2, GroundImpactEvent.Apply row ─────────────────────────────────────────────────
        // | GroundImpactEvent.Apply(Vehicle) | VERIFIED | public void Apply(Vehicle vehicle) |
        // | KSA/GroundImpactEvent.cs:21 | Body only spawns FX and is IsImpactFxSuppressed-gated —
        // | a postfix still fires for every impact, suppressed or not. |
        // Game-thread counterpart of ConstraintSim.DetectTerrainContact (DO NOT PATCH that).
        // ImpactVelocity is NEW IN 5168 (r5162) and is the closing NORMAL speed, not total speed.
        Install(harmony, "GroundImpactEvent.Apply",
            AccessTools.Method(typeof(GroundImpactEvent), nameof(GroundImpactEvent.Apply)),
            postfix: nameof(GroundImpactApplyPostfix));

        // ── §2, WaterSplashEvent row ────────────────────────────────────────────────────────
        // | WaterSplashEvent | VERIFIED | public void Apply(Vehicle vehicle) | KSA/WaterSplashEvent.cs:13 |
        // | No ImpactVelocity — the v ≈ √(2E/m) reconstruction is required. |
        // Game-thread counterpart of VehicleUpdateTask.DetectWaterSplash (DO NOT PATCH that).
        Install(harmony, "WaterSplashEvent.Apply",
            AccessTools.Method(typeof(WaterSplashEvent), nameof(WaterSplashEvent.Apply)),
            postfix: nameof(WaterSplashApplyPostfix));

        // ── §2, Vehicle.Recover row ─────────────────────────────────────────────────────────
        // | Vehicle.Recover() | VERIFIED | public void Recover() | KSA/Vehicle.cs:2765 |
        // | Ends crew missions inline, then enqueues VehicleDestroyData{Recovered = true}. |
        // Recover only marks intent here; the flight.ended is emitted from Vehicle.Dispose below,
        // which is the single removal choke point, with this flag choosing the reason.
        Install(harmony, "Vehicle.Recover",
            AccessTools.Method(typeof(Vehicle), nameof(Vehicle.Recover)),
            prefix: nameof(RecoverPrefix));

        // ── §2, "Deregister counterpart" row (and B12) ──────────────────────────────────────
        // | CHANGED (better hook exists) | The true single removal choke point is
        // | public void Dispose(bool endMission) KSA/Vehicle.cs:3510, which deregisters at :3520
        // | and covers destroy / dock-consume / EVA-board / shutdown. |
        // The parameterless Dispose() delegates to this overload, so patching the bool one alone
        // catches every path exactly once.
        Install(harmony, "Vehicle.Dispose(bool)",
            AccessTools.Method(typeof(Vehicle), nameof(Vehicle.Dispose), [typeof(bool)]),
            prefix: nameof(DisposePrefix));

        // ── §2, Vehicle.KillCrew row (and §4) ───────────────────────────────────────────────
        // | Vehicle.KillCrew | VERIFIED | public void KillCrew() | KSA/Vehicle.cs:2796 |
        // | Exactly one caller. |
        // §4: KillCrew is reached only from InputEvents.cs:515, guarded by if (!Recovered) — i.e.
        // exclusively from a player-initiated destroy. It is the only path in the entire game that
        // sets KittenRosterEntryData.Kia. So this is a PLAYER-INTENT marker, not a fatality signal;
        // it timestamps the manual destroy so the next roster diff can label the KIA correctly.
        Install(harmony, "Vehicle.KillCrew",
            AccessTools.Method(typeof(Vehicle), nameof(Vehicle.KillCrew)),
            prefix: nameof(KillCrewPrefix));

        // ── §2, SequenceList row ────────────────────────────────────────────────────────────
        // | SequenceList.ActivateNextSequence(Vehicle) | VERIFIED | public void
        // | ActivateNextSequence(Vehicle vehicle) | KSA/SequenceList.cs:135 |
        // | Only call site in the whole game: Vehicle.cs:3342, behind the stage key. |
        // Postfix, so ActiveSequence is the newly-activated one.
        Install(harmony, "SequenceList.ActivateNextSequence",
            AccessTools.Method(typeof(SequenceList), nameof(SequenceList.ActivateNextSequence)),
            postfix: nameof(ActivateNextSequencePostfix));

        // ── §2, DockingPort.Dock row + "Dock/undock hook choice" row ────────────────────────
        // | public Vehicle? Dock(Vehicle thisVehicle, Vehicle otherVehicle, DockingPort
        // | otherVehicleDockingPort, out PoseChange consumedToCombined) | KSA/DockingPort.cs:422 |
        // Patched directly rather than DockingEvent.Apply because DockingEvent covers only
        // PHYSICS-initiated docking; player-commanded dock/undock goes through InputEvents instead.
        // All call sites are game-thread. (DockingEvent is also suppressed when a destruction is
        // pending the same frame — VehicleUpdateTask.cs:415-416.)
        Install(harmony, "DockingPort.Dock",
            AccessTools.Method(typeof(DockingPort), nameof(DockingPort.Dock)),
            postfix: nameof(DockPostfix));

        // ── §2, DockingPort.Undock row ──────────────────────────────────────────────────────
        // | public Vehicle? Undock(Vehicle oldVehicle, out PoseChange combinedToSplit) |
        // | KSA/DockingPort.cs:460 | Body: oldVehicle.Split(...). Caller: InputEvents.cs:384. |
        Install(harmony, "DockingPort.Undock",
            AccessTools.Method(typeof(DockingPort), nameof(DockingPort.Undock)),
            postfix: nameof(UndockPostfix));

        // ── §2, EVADoor.CreateKittenEva row (and B1) ────────────────────────────────────────
        // | CHANGED — name collision | private KittenEva? CreateKittenEva(Vehicle, IVASeat,
        // | KittenRosterEntryData) — private instance | KSA/EVADoor.cs:133 |
        // B1: TWO different methods share this name. The public static
        // KittenEva.CreateKittenEva(CelestialSystem, VehicleTemplate, IParentBody, string) is the
        // scenario/template spawn path and its id is NOT guaranteed to be a roster name. The
        // declaring type in the AccessTools call below is what disambiguates them.
        Install(harmony, "EVADoor.CreateKittenEva",
            AccessTools.Method(typeof(EVADoor), "CreateKittenEva"),
            postfix: nameof(CreateKittenEvaPostfix));

        // ── §2, Vehicle.Teleport row (and B7) ───────────────────────────────────────────────
        // | Vehicle.Teleport | VERIFIED | public void Teleport(Orbit?, doubleQuat?, double3?) |
        // | KSA/Vehicle.cs:2031 | This is the real mutation point. |
        // | Vehicle.TeleportToLocation | CHANGED (does not teleport) | Enqueues
        // | InputEvents.TeleportInputData at :3927; the actual teleport happens in
        // | TeleportInputData.Apply() KSA/InputEvents.cs:295. |
        //
        // B7 offers "Vehicle.Teleport and/or InputEvents.TeleportInputData.Apply()". Vehicle.Teleport
        // is the wrong one: it has three callers and only one of them is the player cheating —
        // EVADoor.cs:158 teleports a kitten as part of NORMAL EVA egress, and VehicleEditor.cs:2193
        // teleports the newly split vehicle on an editor decouple. Flagging on Vehicle.Teleport
        // would therefore taint every EVA and every editor split, i.e. exclude ordinary play from
        // the boards. TeleportInputData.Apply is the player-command path and only that: its two
        // producers are Vehicle.TeleportToLocation (:3920, the console/right-click "teleport to
        // location") and the Set Orbit debug window (:4724). Patching it also satisfies B7's
        // warning that TeleportToLocation alone misses the console/UI paths, since both funnel here.
        //
        // TeleportInputData is a STRUCT, so Harmony passes __instance by ref.
        Install(harmony, "InputEvents.TeleportInputData.Apply",
            AccessTools.Method(typeof(InputEvents.TeleportInputData), nameof(InputEvents.TeleportInputData.Apply)),
            prefix: nameof(TeleportPrefix));

        // ── §2, Vehicle.RefillConsumables row ───────────────────────────────────────────────
        // | Vehicle.RefillConsumables | VERIFIED | public void RefillConsumables() |
        // | KSA/Vehicle.cs:2981 | Companion DepleteConsumables() :2988 — patch it too. |
        // The terminal `refill`/`empty` commands reach these transitively through
        // InputEvents.VehicleResourcesChangeData.Apply() (InputEvents.cs:542), so these two rows
        // cover the console path as well and no struct-instance patch is needed.
        Install(harmony, "Vehicle.RefillConsumables",
            AccessTools.Method(typeof(Vehicle), nameof(Vehicle.RefillConsumables)),
            prefix: nameof(RefillConsumablesPrefix));

        Install(harmony, "Vehicle.DepleteConsumables",
            AccessTools.Method(typeof(Vehicle), nameof(Vehicle.DepleteConsumables)),
            prefix: nameof(DepleteConsumablesPrefix));

        // ── §2, Universe.Destroy terminal command (§4 producer table) ───────────────────────
        // | Universe.Destroy(string id) terminal command | KSA/Universe.cs:1126-1130 |
        // | Recovered = false ⇒ KIA |
        // [TerminalAction("destroy", ...)] private static void Destroy(string id) — Universe.cs:1107.
        // A console command that removes a vehicle taints the flight: flag it before the destroy.
        Install(harmony, "Universe.Destroy(string)",
            AccessTools.Method(typeof(Universe), "Destroy", [typeof(string)]),
            prefix: nameof(UniverseDestroyPrefix));

        // ── §2, Universe.DeserializeSave row ────────────────────────────────────────────────
        // | Universe.DeserializeSave | VERIFIED | public static void DeserializeSave(UniverseData) |
        // | KSA/Universe.cs:2140 | Runs AFTER CurrentSystem.DestroyAllVehicles() — a true
        // | teardown+rebuild boundary. Sets KittenRoster = universeData.KittenRoster (:2178). |
        // B8: the roster OBJECT is swapped here, so nothing may be cached across this point.
        Install(harmony, "Universe.DeserializeSave",
            AccessTools.Method(typeof(Universe), nameof(Universe.DeserializeSave)),
            postfix: nameof(SessionBoundaryPostfix));

        // ── §2, Universe.LoadSystem row ─────────────────────────────────────────────────────
        // | Universe.LoadSystem | VERIFIED | public static void LoadSystem(string id) |
        // | KSA/Universe.cs:167 | Creates a fresh KittenRosterData (:176), calls
        // | AssignStartingCrew() (:181), then OnLoaded() (:178). |
        // Its only caller is the boot path (KSA/Program.cs:965) — there is no runtime "new game"
        // — so this is exactly "a career that has never been saved is starting".
        Install(harmony, "Universe.LoadSystem",
            AccessTools.Method(typeof(Universe), nameof(Universe.LoadSystem)),
            prefix: nameof(LoadSystemPrefix),
            postfix: nameof(SessionBoundaryPostfix));

        // ── career identity (§4.1) ──────────────────────────────────────────────────────────
        // KSA has no save/career/player id anywhere; the save's own folder name is the only
        // stable per-save string that exists (see VehicleTelemetry's career section for the
        // decomp citations). These two patches are the only places it is reachable.
        //
        // | UncompressedSave.Load() | VERIFIED | public override void Load() |
        // | KSA/UncompressedSave.cs:45 | Instance, zero args — __instance carries Id. |
        // PREFIX, not postfix: Load() calls Universe.DeserializeSave (:57) itself, so the
        // SessionBoundaryPostfix above fires *inside* this method. The career has to be adopted
        // before that, or the first session of every load would be stamped with the previous
        // career's id.
        Install(harmony, "UncompressedSave.Load",
            AccessTools.Method(typeof(UncompressedSave), nameof(UncompressedSave.Load)),
            prefix: nameof(SaveLoadPrefix));

        // | UncompressedSave.Make(string) | VERIFIED | public static GameSave Make(string name) |
        // | KSA/UncompressedSave.cs:104 | The single write path: the terminal `save <name>`
        // | command (KSA/GameSaves.cs:252) and the UI popup (KSA/GameSaves.cs:229) both land here,
        // | and Overwrite() is Delete()+Make() (:85-89). |
        // Postfix so the career only moves once the save actually exists on disk. This is what
        // lets a career that began unsaved keep its identity across the first save, and what
        // carries a career through a "save as".
        Install(harmony, "UncompressedSave.Make",
            AccessTools.Method(typeof(UncompressedSave), nameof(UncompressedSave.Make), [typeof(string)]),
            postfix: nameof(SaveMakePostfix));
    }

    private static void Install(
        Harmony harmony,
        string label,
        MethodBase? original,
        string? prefix = null,
        string? postfix = null)
    {
        // The null check is the whole point: the decompiled sources can lag the shipping binary
        // (docs/ksa-integration.md §6, "runtime field-name fidelity — UNVERIFIABLE from source"),
        // so a renamed or removed target must degrade to "this one signal is missing", not to a
        // NullReferenceException inside the mod loader.
        if (original is null)
        {
            UnresolvedTargets.Add(label);
            ModLog.Log.Warn(
                $"catlog: patch target '{label}' does not exist in this game build; that signal will not be "
                + "recorded this session. docs/ksa-integration.md needs re-verifying.");
            return;
        }

        try
        {
            MethodInfo? prefixMethod = prefix is null ? null : Resolve(prefix);
            MethodInfo? postfixMethod = postfix is null ? null : Resolve(postfix);
            harmony.Patch(
                original,
                prefix: prefixMethod is null ? null : new HarmonyMethod(prefixMethod),
                postfix: postfixMethod is null ? null : new HarmonyMethod(postfixMethod));

            if (prefixMethod is not null)
                Applied.Add(new Installed(original, prefixMethod));
            if (postfixMethod is not null)
                Applied.Add(new Installed(original, postfixMethod));

            ModLog.Log.Debug($"catlog: patched {label} ({original.DeclaringType?.FullName}.{original.Name}).");
        }
        catch (Exception ex)
        {
            UnresolvedTargets.Add(label);
            ModLog.Log.Warn($"catlog: could not patch '{label}': {ex.Message}");
        }
    }

    private static MethodInfo Resolve(string name)
        => typeof(Patcher).GetMethod(name, BindingFlags.NonPublic | BindingFlags.Static)
           ?? throw new MissingMethodException(nameof(Patcher), name);

    // ───────────────────────────── patch bodies ─────────────────────────────
    // All static, all wrapped, none ever throws into the game.

    private static void ApplyVehicleSolversPostfix()
    {
        // Deliberately almost empty: the value is the heartbeat, not a side effect. Closing the
        // catlog frame here would be premature (manual destroys land in ApplyInputEvents six lines
        // later at Program.cs:1918), and doing real work in it would put catlog on the critical
        // path of every physics batch.
        SolverBatches++;
    }

    /// <summary>How many solver batches have been applied since load — the "is the game feeding us" heartbeat.</summary>
    public static long SolverBatches { get; private set; }

    [KsaAnchor("Universe.LoadSystem(string)",
        SourceFile = "KSA/Universe.cs:167-179", Verified = "2026-08-10",
        GameVersion = "2026.8.5.5168", Risk = ChurnRisk.Low,
        Notes = "Postfix is required: CurrentSystem is assigned at line 174 after construction.")]
    private static void LoadSystemPostfix()
    {
        try
        {
            SystemSurvey.CaptureCurrent();
        }
        catch (Exception ex)
        {
            ModLog.Log.Warn($"catlog: system survey failed: {ex.Message}");
        }
    }

    private static void DestroyVehicleFromEventPrefix(Vehicle vehicle, VehicleDestructionEvent destructionEvent)
    {
        if (_runtime is not { } runtime)
            return;

        try
        {
            // Universe.cs:1701 early-returns on IsDisposed, so a second call is a no-op for the
            // game; guard the same way rather than emitting a duplicate RUD.
            if (vehicle.IsDisposed)
                return;

            string id = Track(runtime, vehicle, out double simT, out long wallMs);
            if (id.Length == 0)
                return;

            Destroying.Add(id);
            runtime.Signal(new RudSignal(
                simT,
                wallMs,
                id,
                VehicleTelemetry.MapCause(destructionEvent.Cause),
                PeakG: destructionEvent.PeakGLoad,
                PeakQPa: destructionEvent.PeakDynamicPressure,
                SpeedMs: VehicleTelemetry.SurfaceSpeedMs(vehicle),
                AltitudeM: VehicleTelemetry.AltitudeM(vehicle),
                Body: VehicleTelemetry.BodyOf(vehicle),
                // §4/D11: everyone survives. EndAllCrewMissions banks the mission and frees the
                // kitten; the physics path never reaches KillCrew.
                CrewCount: VehicleTelemetry.CrewCount(vehicle),
                // Prefix: the vehicle is fully intact (Universe.cs:1699), so the position is the
                // position it died at rather than a torn-down zero.
                Lat: VehicleTelemetry.Latitude(vehicle),
                Lon: VehicleTelemetry.Longitude(vehicle)));
        }
        catch (Exception ex)
        {
            NoteBodyError(ex);
        }
    }

    private static void GroundImpactApplyPostfix(GroundImpactEvent __instance, Vehicle vehicle)
    {
        if (_runtime is not { } runtime)
            return;

        try
        {
            // The game applies the event whether or not FX are suppressed, so the 5 s post-teleport
            // window has to be checked here (Vehicle.cs:5271). An impact inside it is not a real
            // lithobrake.
            if (VehicleTelemetry.IsImpactSuppressed(vehicle))
                return;

            string id = Track(runtime, vehicle, out double simT, out long wallMs);
            if (id.Length == 0)
                return;

            runtime.Signal(new ImpactSignal(
                simT,
                wallMs,
                id,
                // ImpactVelocity is the closing NORMAL speed in m/s (ConstraintSim.cs:726-738),
                // new in 5168 — not the vehicle's total speed.
                SpeedMs: __instance.ImpactVelocity,
                EnergyJ: __instance.ImpactKineticEnergy,
                LaunchPad: __instance.IsLaunchPad,
                Body: VehicleTelemetry.BodyOf(vehicle),
                CrewCount: VehicleTelemetry.CrewCount(vehicle),
                Lat: VehicleTelemetry.Latitude(vehicle),
                Lon: VehicleTelemetry.Longitude(vehicle)));
        }
        catch (Exception ex)
        {
            NoteBodyError(ex);
        }
    }

    private static void WaterSplashApplyPostfix(WaterSplashEvent __instance, Vehicle vehicle)
    {
        if (_runtime is not { } runtime)
            return;

        try
        {
            if (VehicleTelemetry.IsImpactSuppressed(vehicle))
                return;

            string id = Track(runtime, vehicle, out double simT, out long wallMs);
            if (id.Length == 0)
                return;

            // WaterSplashEvent carries no velocity (§2), so it is reconstructed from the kinetic
            // energy the detector computed as 0.5 * TotalMass * v². A zero or unknown mass yields
            // 0 rather than an infinity.
            double mass = VehicleTelemetry.MassKg(vehicle);
            double energy = __instance.ImpactKineticEnergy;
            double speed = mass > 0 && energy > 0 ? Math.Sqrt(2.0 * energy / mass) : 0.0;

            runtime.Signal(new SplashSignal(
                simT, wallMs, id, speed, energy,
                VehicleTelemetry.BodyOf(vehicle), VehicleTelemetry.CrewCount(vehicle),
                VehicleTelemetry.Latitude(vehicle), VehicleTelemetry.Longitude(vehicle)));
        }
        catch (Exception ex)
        {
            NoteBodyError(ex);
        }
    }

    private static void RecoverPrefix(Vehicle __instance)
    {
        if (_runtime is null)
            return;

        try
        {
            string id = VehicleTelemetry.IdOf(__instance);
            if (id.Length > 0)
                Recovering.Add(id);
        }
        catch (Exception ex)
        {
            NoteBodyError(ex);
        }
    }

    private static void DisposePrefix(Vehicle __instance, bool endMission)
    {
        if (_runtime is not { } runtime)
            return;

        try
        {
            string id = VehicleTelemetry.IdOf(__instance);
            if (id.Length == 0)
                return;

            bool recovered = Recovering.Remove(id);
            bool destroyed = Destroying.Remove(id);

            // Only close a flight we actually opened. Forget returns false for a vehicle catlog
            // never saw, and inventing a flight.ended for it would create a flight with no
            // flight.started for the server to join against.
            if (!runtime.Forget(id))
                return;

            FlightEndReason reason = recovered
                ? FlightEndReason.Recovered
                : destroyed
                    ? FlightEndReason.Destroyed
                    // Docking merge, EVA board, save teardown: the vehicle left the simulation but
                    // nothing destroyed it. endMission being false is the game's own "this is a
                    // structural removal, not a mission end" hint (Vehicle.cs:3510-3517).
                    : FlightEndReason.Despawned;

            double simT = VehicleTelemetry.SimTimeSeconds();
            long wallMs = DateTimeOffset.UtcNow.ToUnixTimeMilliseconds();

            // A KittenEva's Vehicle.Id IS the kitten's roster name, so its disposal is the end of
            // that kitten's EVA (§2, "EVA vehicle Id is the kitten's roster name").
            if (VehicleTelemetry.IsKitten(__instance))
            {
                double launch = VehicleTelemetry.LaunchGameTime(__instance);
                double duration = double.IsNaN(launch) ? 0.0 : Math.Max(0.0, simT - launch);
                runtime.Signal(new EvaEndSignal(simT, wallMs, id, duration));
            }

            // This is a PREFIX on the single removal choke point, so the vehicle has not been torn
            // down yet: its orbit, its parent and its seats are all still readable, and this is the
            // last instant at which they are. That is what makes flight.ended able to carry a body
            // and a position at all — a postfix here would have nothing left to read, and a value
            // recovered from the last telemetry sample could be up to half a second and one SOI
            // change stale.
            runtime.Signal(new VehicleRemovedSignal(
                simT,
                wallMs,
                id,
                reason,
                VehicleTelemetry.CrewCount(__instance),
                VehicleTelemetry.BodyOf(__instance),
                VehicleTelemetry.CrewNames(__instance),
                VehicleTelemetry.Latitude(__instance),
                VehicleTelemetry.Longitude(__instance)));
        }
        catch (Exception ex)
        {
            NoteBodyError(ex);
        }
    }

    private static void KillCrewPrefix(Vehicle __instance)
    {
        if (_runtime is not { } runtime)
            return;

        try
        {
            // Register first, like every other vehicle-scoped patch body: a vehicle created and
            // destroyed inside one sample interval still owes a flight.started ahead of anything
            // that names its flight.
            string id = Track(runtime, __instance, out double simT, out long wallMs);
            if (id.Length > 0)
            {
                Destroying.Add(id);

                // The seats are readable here and nowhere afterwards — Vehicle.Dispose follows in
                // the same frame, and the roster diff that raises kitten.kia a tick later sees a
                // name and nothing else. Reading them now is what lets those deaths be attributed
                // to this flight, which is what the §4.2 ±2 s window needs to disqualify anything.
                runtime.Signal(new CrewKilledSignal(simT, wallMs, id, VehicleTelemetry.CrewNames(__instance)));
            }

            // The roster rows flip to Kia inside this call; timestamping it lets the next poll
            // label the resulting kitten.kia as manual_destroy rather than unknown.
            runtime.NoteManualDestroy(simT);
        }
        catch (Exception ex)
        {
            NoteBodyError(ex);
        }
    }

    private static void ActivateNextSequencePostfix(SequenceList __instance, Vehicle vehicle)
    {
        if (_runtime is not { } runtime)
            return;

        try
        {
            string id = Track(runtime, vehicle, out double simT, out long wallMs);
            if (id.Length == 0)
                return;

            runtime.Signal(new StagingSignal(simT, wallMs, id, __instance.ActiveSequence));
        }
        catch (Exception ex)
        {
            NoteBodyError(ex);
        }
    }

    private static void DockPostfix(Vehicle thisVehicle, Vehicle otherVehicle, Vehicle? __result)
    {
        if (_runtime is not { } runtime)
            return;

        try
        {
            // Dock returns null when the port was already docked — nothing happened.
            if (__result is null)
                return;

            string id = Track(runtime, thisVehicle, out double simT, out long wallMs);
            string other = Track(runtime, otherVehicle, out _, out _);
            if (id.Length == 0 || other.Length == 0)
                return;

            runtime.Signal(new DockSignal(simT, wallMs, id, other));
        }
        catch (Exception ex)
        {
            NoteBodyError(ex);
        }
    }

    private static void UndockPostfix(Vehicle oldVehicle, Vehicle? __result)
    {
        if (_runtime is not { } runtime)
            return;

        try
        {
            if (__result is null)
                return;

            string id = Track(runtime, oldVehicle, out double simT, out long wallMs);
            string split = Track(runtime, __result, out _, out _);
            if (id.Length == 0)
                return;

            runtime.Signal(new UndockSignal(simT, wallMs, id, split));
        }
        catch (Exception ex)
        {
            NoteBodyError(ex);
        }
    }

    private static void CreateKittenEvaPostfix(KittenEva? __result, KittenRosterEntryData rosterEntry)
    {
        if (_runtime is not { } runtime)
            return;

        try
        {
            // Null means the door could not produce an EVA (no backpack part) — no egress happened.
            if (__result is null)
                return;

            double simT = VehicleTelemetry.SimTimeSeconds();
            long wallMs = DateTimeOffset.UtcNow.ToUnixTimeMilliseconds();
            string name = rosterEntry.Name;
            if (string.IsNullOrEmpty(name))
                return;

            runtime.Signal(new EvaStartSignal(simT, wallMs, name, VehicleTelemetry.IdOf(__result)));
        }
        catch (Exception ex)
        {
            NoteBodyError(ex);
        }
    }

    private static void TeleportPrefix(ref InputEvents.TeleportInputData __instance)
    {
        // The read is inside the try like every other patch body's: Flag catches its own work, but
        // __instance.Vehicle is a KSA read on the far side of a Harmony trampoline, and a patch
        // body that throws is a hard failure in the game rather than a logged one in catlog.
        try
        {
            Vehicle? vehicle = __instance.Vehicle;
            if (vehicle is not null)
                Flag(vehicle, FlightFlag.Teleport, "the vehicle was teleported by a player command");
        }
        catch (Exception ex)
        {
            NoteBodyError(ex);
        }
    }

    private static void RefillConsumablesPrefix(Vehicle __instance)
        => Flag(__instance, FlightFlag.Refuel, "Vehicle.RefillConsumables was called");

    private static void DepleteConsumablesPrefix(Vehicle __instance)
        => Flag(__instance, FlightFlag.ResourceEdit, "Vehicle.DepleteConsumables was called");

    private static void UniverseDestroyPrefix(string id)
    {
        if (_runtime is not { } runtime || string.IsNullOrEmpty(id))
            return;

        try
        {
            runtime.Signal(new FlaggedSignal(
                VehicleTelemetry.SimTimeSeconds(),
                DateTimeOffset.UtcNow.ToUnixTimeMilliseconds(),
                id,
                FlightFlag.Console,
                "the terminal `destroy` command was used on this vehicle"));
        }
        catch (Exception ex)
        {
            NoteBodyError(ex);
        }
    }

    private static void SessionBoundaryPostfix()
    {
        try
        {
            _runtime?.OnSessionBoundary();
        }
        catch (Exception ex)
        {
            NoteBodyError(ex);
        }
    }

    /// <summary>A brand-new game is being built: start a career that has never been saved (§4.1).</summary>
    private static void LoadSystemPrefix()
    {
        try
        {
            VehicleTelemetry.BeginUnsavedCareer();
        }
        catch (Exception ex)
        {
            NoteBodyError(ex);
        }
    }

    /// <summary>A save is about to be deserialised: adopt its career before the session boundary.</summary>
    /// <param name="__instance">The save being loaded.</param>
    private static void SaveLoadPrefix(UncompressedSave __instance)
    {
        try
        {
            VehicleTelemetry.AdoptSaveCareer(__instance);
        }
        catch (Exception ex)
        {
            NoteBodyError(ex);
        }
    }

    /// <summary>A save was just written: the career now lives in that slot (§4.1).</summary>
    /// <param name="__result">The save that was written.</param>
    private static void SaveMakePostfix(GameSave __result)
    {
        try
        {
            VehicleTelemetry.AdoptSaveCareer(__result);
        }
        catch (Exception ex)
        {
            NoteBodyError(ex);
        }
    }

    private static void Flag(Vehicle vehicle, FlightFlag flag, string detail)
    {
        if (_runtime is not { } runtime)
            return;

        try
        {
            string id = Track(runtime, vehicle, out double simT, out long wallMs);
            if (id.Length == 0)
                return;
            runtime.Signal(new FlaggedSignal(simT, wallMs, id, flag, detail));
        }
        catch (Exception ex)
        {
            NoteBodyError(ex);
        }
    }

    // Every vehicle-scoped signal goes through this so a vehicle catlog has not sampled yet still
    // gets its flight.started emitted first, in the same order (see PolledSignals.Track).
    private static string Track(CatlogRuntime runtime, Vehicle vehicle, out double simT, out long wallMs)
    {
        simT = VehicleTelemetry.SimTimeSeconds();
        wallMs = DateTimeOffset.UtcNow.ToUnixTimeMilliseconds();

        Scratch.Clear();
        string id = runtime.Track(vehicle, simT, wallMs, Scratch);
        foreach (GameSignal signal in Scratch)
            runtime.Signal(signal);
        Scratch.Clear();
        return id;
    }

    private static void NoteBodyError(Exception ex)
    {
        if (_bodyErrorLogged)
            return;
        _bodyErrorLogged = true;
        ModLog.Log.Warn($"catlog: a Harmony patch body faulted (logged once this session): {ex.Message}");
    }

    private readonly record struct Installed(MethodBase Original, MethodInfo Patch);
}
