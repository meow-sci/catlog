using System;
using System.Collections.Generic;
using KSA;
using MeowSci.Catlog.Lib.Events;
using MeowSci.Catlog.Lib.Telemetry;
using MeowSci.Catlog.Lib.Util;

namespace MeowSci.Catlog;

/// <summary>
/// <b>Every KSA read in the mod lives here.</b> Nothing else in <c>mod/catlog</c> — not
/// <see cref="Patcher"/>, not <see cref="CatlogRuntime"/>, not the status window — touches a game
/// type for a value; they call into this class and get plain primitives back.
/// </summary>
/// <remarks>
/// <para>
/// That is the whole point of the <c>catlog.lib</c> / <c>catlog</c> split: when a game build moves
/// a member, the compiler fails inside this file and <see cref="Patcher"/>, the
/// <see cref="KsaAnchorAttribute"/> on each method says what it was bound to and when it was last
/// checked, and no logic has to be re-verified.
/// </para>
/// <para>
/// <b>Failure policy (WP7 requirement 7):</b> a per-vehicle read failure means the vehicle is
/// <b>omitted from the frame</b>, never zero-filled. A zeroed fallback snapshot fed to a prev/curr
/// comparator manufactures phantom SOI changes (body → <c>""</c>) and phantom orbit-achieved edges
/// (eccentricity → 0), and both of those score on a leaderboard.
/// </para>
/// </remarks>
public static class VehicleTelemetry
{
    private const double RadToDeg = 180.0 / Math.PI;
    private const double StandardGravity = 9.80665;

    /// <summary>The stock <c>KittenLocomotionTuning.TumbleSpeedGate</c>, in m/s.</summary>
    /// <remarks>
    /// <c>docs/ksa-integration.md</c> B9: the field is a mutable public static that the game's own
    /// "Kitten Locomotion Tuning" debug window live-edits, and it is the sole classifier for
    /// <c>kitten.tumble</c>. Any deviation from this constant flags the session.
    /// </remarks>
    public const float StockTumbleSpeedGate = 6.5f;

    private static string _gameBuild = string.Empty;

    /// <summary>
    /// The running game's build string, e.g. <c>2026.8.5.5168</c> — the real one, read from the
    /// binary, so <c>session.started.game_build</c> is never a stale hard-coded literal.
    /// </summary>
    /// <remarks>
    /// <c>VersionInfo.VersionString</c> is formatted <c>$"v{Major}.{Minor}.{Build}.{Revision}{Suffix}"</c>,
    /// so the leading <c>v</c> is stripped to match the wire form used everywhere else in catlog.
    /// Cached after the first successful read; <c>"unknown"</c> when the read throws, which is
    /// honest and lets the server see that this batch's build is unattributable.
    /// </remarks>
    /// <returns>The build string, or <c>"unknown"</c>.</returns>
    [KsaAnchor("VersionInfo.Current.VersionString",
        SourceFile = "KSA/VersionInfo.cs:115", Verified = "2026-08-07", GameVersion = "2026.8.5.5168",
        Risk = ChurnRisk.Low,
        Notes = "Formatted 'v{Major}.{Minor}.{Build}.{Revision}{Suffix}' at VersionInfo.cs:143 — the leading "
                + "'v' is stripped. Cached after the first read.")]
    public static string GameBuild()
    {
        if (_gameBuild.Length > 0)
            return _gameBuild;

        try
        {
            string raw = VersionInfo.Current.VersionString ?? string.Empty;
            _gameBuild = raw.StartsWith('v') ? raw[1..] : raw;
        }
        catch (Exception ex)
        {
            ModLog.Log.Warn($"catlog: could not read the game build string: {ex.Message}");
            _gameBuild = string.Empty;
        }

        return _gameBuild.Length > 0 ? _gameBuild : "unknown";
    }

    /// <summary>
    /// True when a vehicle can be read at all. An uninitialized vehicle's <c>Orbit</c> chains
    /// through <c>FlightPlan.Patches[0]</c> and <b>throws</b> <see cref="ArgumentOutOfRangeException"/>
    /// rather than returning null (<c>docs/ksa-integration.md</c> B6), so this guard runs first
    /// everywhere.
    /// </summary>
    /// <param name="vehicle">The vehicle.</param>
    /// <returns>True when the vehicle's orbit-derived state is safe to touch.</returns>
    [KsaAnchor("Vehicle.IsDisposed; Vehicle.FlightPlan.Patches.Count",
        SourceFile = "KSA/Vehicle.cs:602,450 / KSA/FlightPlan.cs:64", Verified = "2026-08-07",
        GameVersion = "2026.8.5.5168", Risk = ChurnRisk.Low,
        Notes = "ksa-integration B6: Vehicle.Parent => Orbit.Parent => Patch => FlightPlan.Patches[0]. "
                + "Patches is a List<PatchedConic> that is empty until the vehicle is initialized.")]
    public static bool IsReadable(Vehicle? vehicle)
    {
        if (vehicle is null || vehicle.IsDisposed)
            return false;

        try
        {
            return vehicle.FlightPlan.Patches.Count > 0;
        }
        catch (Exception)
        {
            return false;
        }
    }

    /// <summary>
    /// Samples one vehicle. Returns <c>null</c> when the vehicle cannot be read — the caller then
    /// omits it from the frame entirely.
    /// </summary>
    /// <param name="vehicle">The vehicle.</param>
    /// <param name="simT">Universe sim seconds at capture.</param>
    /// <param name="wallMs">Client unix milliseconds at capture.</param>
    /// <returns>The snapshot, or null.</returns>
    [KsaAnchor("Vehicle.Id/Situation/GetBarometricAltitude/GetSurfaceSpeed/OrbitalSpeed/AccelerationBody/"
               + "TotalMass/Parts.Count/Crew/StructuralLoad; Orbit.Eccentricity/Apoapsis/Periapsis/Inclination/"
               + "IsBound/IsHyperbolic/IsParabolic; IParentBody.Id/MeanRadius/GetAtmosphereReference; "
               + "PhysicalAtmosphereReference.GetDynamicPressure(Vehicle)",
        SourceFile = "KSA/Vehicle.cs / KSA/Orbit.cs / KSA/IParentBody.cs / KSA/PhysicalAtmosphereReference.cs",
        Verified = "2026-08-07", GameVersion = "2026.8.5.5168", Risk = ChurnRisk.Low,
        Notes = "UNITS: Orbit.Inclination is RADIANS (Orbit.cs:1160) — converted to degrees here. "
                + "Orbit.Apoapsis/Periapsis are RADII FROM BODY CENTRE (Orbit.cs:1166-1168) — MeanRadius is "
                + "subtracted here, see ksa-integration §1 and docs/events.md. "
                + "Vehicle.TotalMass is float (Vehicle.cs:551). "
                + "Vehicle.Crew is ALL SEATS, not occupants (Vehicle.cs:373) — occupancy is "
                + "AssignedKittenHash != KeyHash.Zero. "
                + "There is no Vehicle.DynamicPressure property; the static helper is the only path "
                + "(PhysicalAtmosphereReference.cs:66).")]
    public static TelemetrySnapshot? Sample(Vehicle vehicle, double simT, long wallMs)
    {
        if (!IsReadable(vehicle))
            return null;

        try
        {
            Orbit orbit = vehicle.Orbit;
            IParentBody parent = orbit.Parent;
            double meanRadius = parent.MeanRadius;

            OrbitClass conic = ClassifyOrbit(orbit);

            return new TelemetrySnapshot(
                VehicleId: vehicle.Id,
                VehicleName: Ids.SanitizeVehicleName(vehicle.Id),
                SimT: simT,
                WallMs: wallMs,
                Body: BodyName(parent),
                Situation: SituationName(vehicle.Situation),
                AltitudeM: Sanitize.Finite(vehicle.GetBarometricAltitude()),
                SurfaceSpeedMs: Sanitize.Finite(vehicle.GetSurfaceSpeed()),
                OrbitalSpeedMs: Sanitize.Finite(vehicle.OrbitalSpeed),
                AccelMs2: Sanitize.Finite(vehicle.AccelerationBody.Length()),
                MassKg: Sanitize.Finite(vehicle.TotalMass))
            {
                ParentBodyId = BodyName(parent),
                AtmoHeightM = AtmosphereHeightM(parent),
                DynPressurePa = Sanitize.Finite(PhysicalAtmosphereReference.GetDynamicPressure(vehicle)),
                Ecc = Sanitize.Finite(orbit.Eccentricity),
                // A hyperbolic apoapsis is NEGATIVE and a parabolic one is NaN (B4), so an
                // apoapsis altitude is only meaningful on a closed orbit. Sanitize would turn the
                // NaN into 0 but not the negative, so the conic class gates it instead.
                ApAltM = conic == OrbitClass.Bound
                    ? Sanitize.RadiusToAltitude(orbit.Apoapsis, meanRadius)
                    : 0.0,
                PeAltM = Sanitize.RadiusToAltitude(orbit.Periapsis, meanRadius),
                IncDeg = Sanitize.Finite(orbit.Inclination * RadToDeg),
                OrbitClass = conic,
                CrewCount = CrewCount(vehicle),
                PartCount = PartCount(vehicle),
                PeakG = PeakG(vehicle),
                MaxQPa = PeakDynamicPressurePa(vehicle),
            };
        }
        catch (Exception ex)
        {
            // Omit, never zero-fill (WP7 requirement 7). The caller logs once per session.
            Faults.Note(ex);
            return null;
        }
    }

    /// <summary>
    /// The conic class, from the game's own classifiers rather than a NaN sniff on apoapsis
    /// (<c>docs/ksa-integration.md</c> B4).
    /// </summary>
    /// <param name="orbit">The orbit.</param>
    /// <returns>The class, <see cref="OrbitClass.Unknown"/> if the game says none of the three.</returns>
    [KsaAnchor("Orbit.IsParabolic(); Orbit.IsHyperbolic(); Orbit.IsBound()",
        SourceFile = "KSA/Orbit.cs:1757,1763,1775", Verified = "2026-08-07", GameVersion = "2026.8.5.5168",
        Risk = ChurnRisk.Low,
        Notes = "Order matters: parabolic is the ecc == 1 knife-edge and must be tested before the other two.")]
    public static OrbitClass ClassifyOrbit(Orbit orbit)
    {
        if (orbit.IsParabolic())
            return OrbitClass.Parabolic;
        if (orbit.IsHyperbolic())
            return OrbitClass.Hyperbolic;
        return orbit.IsBound() ? OrbitClass.Bound : OrbitClass.Unknown;
    }

    /// <summary>
    /// Peak g-load this physics step, or <c>null</c> when the game has no reading.
    /// </summary>
    /// <remarks>
    /// <c>docs/ksa-integration.md</c> B10: <c>Vehicle.StructuralLoad</c> is written only inside
    /// <c>ApplyFullPhysics</c> and reset to <c>default</c> every prepared step, so an all-zero
    /// struct means "no data this step" (on rails, or in freefall), <b>not</b> "zero g".
    /// <c>MaxGLoad</c> is the discriminator: it is the computed structural limit, always ≥ 5 when
    /// the struct was written (<c>VehicleStructuralLimits.EffectiveMaxGLoad</c> floors at 5), and
    /// exactly 0 when it was not. Reporting 0 here would fill the peak-g board with fake minima.
    /// </remarks>
    /// <param name="vehicle">The vehicle.</param>
    /// <returns>The peak g-load, or null.</returns>
    [KsaAnchor("Vehicle.StructuralLoad (StructuralLoad.PeakGLoad / MaxGLoad)",
        SourceFile = "KSA/Vehicle.cs:531 / KSA/StructuralLoad.cs", Verified = "2026-08-07",
        GameVersion = "2026.8.5.5168", Risk = ChurnRisk.High,
        Notes = "NEW IN BUILD 5168 — did not exist in 5117, so this anchor is one build old. Written at "
                + "VehicleUpdateTask.cs:492-497 (inside DetectStructuralFailure, reached only from "
                + "ApplyFullPhysics); reset at VehicleUpdateState.cs:287.")]
    public static double? PeakG(Vehicle vehicle)
    {
        try
        {
            ref readonly StructuralLoad load = ref vehicle.StructuralLoad;
            if (load.MaxGLoad <= 0.0 || !double.IsFinite(load.PeakGLoad))
                return null;
            return load.PeakGLoad;
        }
        catch (Exception ex)
        {
            Faults.Note(ex);
            return null;
        }
    }

    /// <summary>Peak dynamic pressure this physics step in pascals, or null. Same rule as <see cref="PeakG"/>.</summary>
    /// <param name="vehicle">The vehicle.</param>
    /// <returns>The peak dynamic pressure, or null.</returns>
    [KsaAnchor("Vehicle.StructuralLoad (StructuralLoad.PeakDynamicPressure / MaxDynamicPressure)",
        SourceFile = "KSA/Vehicle.cs:531 / KSA/StructuralLoad.cs", Verified = "2026-08-07",
        GameVersion = "2026.8.5.5168", Risk = ChurnRisk.High,
        Notes = "MaxDynamicPressure is the hard-coded 200 kPa limit set beside PeakDynamicPressure at "
                + "VehicleUpdateTask.cs:495, so it is the same 'was this struct written' discriminator.")]
    public static double? PeakDynamicPressurePa(Vehicle vehicle)
    {
        try
        {
            ref readonly StructuralLoad load = ref vehicle.StructuralLoad;
            if (load.MaxDynamicPressure <= 0.0 || !double.IsFinite(load.PeakDynamicPressure))
                return null;
            return load.PeakDynamicPressure;
        }
        catch (Exception ex)
        {
            Faults.Note(ex);
            return null;
        }
    }

    /// <summary>The lowercase parent body name for the wire's <c>body</c> field.</summary>
    /// <param name="parent">The parent body.</param>
    /// <returns>The lowercase id, or <c>"unknown"</c>.</returns>
    [KsaAnchor("IParentBody.Id (from IObjectId)",
        SourceFile = "KSA/IObjectId.cs:5 / KSA/Astronomical.cs:96", Verified = "2026-08-07",
        GameVersion = "2026.8.5.5168", Risk = ChurnRisk.Low,
        Notes = "Id is NOT declared on IParentBody; it comes from IObjectId.")]
    public static string BodyName(IParentBody? parent)
    {
        string? id = parent?.Id;
        return string.IsNullOrEmpty(id) ? "unknown" : id.ToLowerInvariant();
    }

    /// <summary>The lowercase parent body name of a vehicle, safe against an uninitialized orbit.</summary>
    /// <param name="vehicle">The vehicle.</param>
    /// <returns>The lowercase body id, or <c>"unknown"</c>.</returns>
    public static string BodyOf(Vehicle vehicle)
    {
        try
        {
            return IsReadable(vehicle) ? BodyName(vehicle.Orbit.Parent) : "unknown";
        }
        catch (Exception)
        {
            return "unknown";
        }
    }

    /// <summary>The parent's atmosphere height above mean radius in metres; 0 when airless.</summary>
    /// <param name="parent">The parent body.</param>
    /// <returns>The height in metres.</returns>
    [KsaAnchor("IParentBody.GetAtmosphereReference()?.Physical.Height (DistanceReference)",
        SourceFile = "KSA/IParentBody.cs:57 / KSA/AtmosphereReference.cs:11 / KSA/PhysicalAtmosphereReference.cs:23",
        Verified = "2026-08-07", GameVersion = "2026.8.5.5168", Risk = ChurnRisk.Low,
        Notes = "The whole chain is nullable and Height is a DistanceReference CLASS, not a double — "
                + ".InMeters() (DistanceReference.cs:148). Physical is a FIELD, not a property.")]
    public static double AtmosphereHeightM(IParentBody? parent)
    {
        try
        {
            AtmosphereReference? atmosphere = parent?.GetAtmosphereReference();
            return atmosphere is null ? 0.0 : Sanitize.Finite(atmosphere.Physical.Height.InMeters());
        }
        catch (Exception)
        {
            return 0.0;
        }
    }

    /// <summary>
    /// How many crew seats are <b>occupied</b>. An EVA kitten is its own vehicle and always counts
    /// as one.
    /// </summary>
    /// <param name="vehicle">The vehicle.</param>
    /// <returns>The occupied-seat count.</returns>
    [KsaAnchor("Vehicle.Crew (ReadOnlySpan<IVASeat>); IVASeat.AssignedKittenHash; KittenEva",
        SourceFile = "KSA/Vehicle.cs:373 / KSA/IVASeat.cs:46 / KSA/KittenEva.cs:8", Verified = "2026-08-07",
        GameVersion = "2026.8.5.5168", Risk = ChurnRisk.Low,
        Notes = "Vehicle.Crew is every seat, occupied or not; there is no CrewCount property. The game's own "
                + "occupancy test is AssignedKittenHash != KeyHash.Zero (IVASeat.cs:96-109).")]
    public static int CrewCount(Vehicle vehicle)
    {
        try
        {
            if (vehicle is KittenEva)
                return 1;

            int occupied = 0;
            foreach (IVASeat seat in vehicle.Crew)
            {
                if (seat.AssignedKittenHash != KeyHash.Zero)
                    occupied++;
            }

            return occupied;
        }
        catch (Exception)
        {
            return 0;
        }
    }

    /// <summary>The roster names of the kittens currently seated on a vehicle.</summary>
    /// <param name="vehicle">The vehicle.</param>
    /// <returns>The names; empty when the vehicle carries nobody.</returns>
    [KsaAnchor("Vehicle.Crew; IVASeat.AssignedKittenHash; Universe.KittenRoster.Find(KeyHash); Vehicle.Id (KittenEva)",
        SourceFile = "KSA/Vehicle.cs:373 / KSA/IVASeat.cs:46 / KSA/KittenRosterData.cs:77", Verified = "2026-08-07",
        GameVersion = "2026.8.5.5168", Risk = ChurnRisk.Low,
        Notes = "A KittenEva's Vehicle.Id IS the kitten's roster name (EVADoor.cs:142 → Astronomical.cs:153); "
                + "ksa-integration §2 records the two caveats (rename, template spawn).")]
    public static IReadOnlyList<string> CrewNames(Vehicle vehicle)
    {
        try
        {
            if (vehicle is KittenEva)
                return string.IsNullOrEmpty(vehicle.Id) ? [] : [vehicle.Id];

            List<string>? names = null;
            foreach (IVASeat seat in vehicle.Crew)
            {
                if (seat.AssignedKittenHash == KeyHash.Zero)
                    continue;
                KittenRosterEntryData? entry = Universe.KittenRoster.Find(seat.AssignedKittenHash);
                if (entry is not null && !string.IsNullOrEmpty(entry.Name))
                    (names ??= []).Add(entry.Name);
            }

            return names ?? (IReadOnlyList<string>)[];
        }
        catch (Exception)
        {
            return [];
        }
    }

    /// <summary>The vehicle's part count.</summary>
    /// <param name="vehicle">The vehicle.</param>
    /// <returns>The part count, 0 when unreadable.</returns>
    [KsaAnchor("Vehicle.Parts.Count",
        SourceFile = "KSA/Vehicle.cs:589 / KSA/PartTree.cs:89", Verified = "2026-08-07",
        GameVersion = "2026.8.5.5168", Risk = ChurnRisk.Low)]
    public static int PartCount(Vehicle vehicle)
    {
        try
        {
            return vehicle.Parts.Count;
        }
        catch (Exception)
        {
            return 0;
        }
    }

    /// <summary>Total mass in kilograms.</summary>
    /// <param name="vehicle">The vehicle.</param>
    /// <returns>The mass, 0 when unreadable.</returns>
    [KsaAnchor("Vehicle.TotalMass (float)",
        SourceFile = "KSA/Vehicle.cs:551", Verified = "2026-08-07", GameVersion = "2026.8.5.5168",
        Risk = ChurnRisk.Low)]
    public static double MassKg(Vehicle vehicle)
    {
        try
        {
            return Sanitize.Finite(vehicle.TotalMass);
        }
        catch (Exception)
        {
            return 0.0;
        }
    }

    /// <summary>Barometric altitude above the parent's mean radius, in metres.</summary>
    /// <param name="vehicle">The vehicle.</param>
    /// <returns>The altitude, 0 when unreadable.</returns>
    [KsaAnchor("Vehicle.GetBarometricAltitude()",
        SourceFile = "KSA/Vehicle.cs:2840", Verified = "2026-08-07", GameVersion = "2026.8.5.5168",
        Risk = ChurnRisk.Low,
        Notes = "Above MEAN RADIUS, not terrain; GetRadarAltitude() (:2845) is the terrain-relative one.")]
    public static double AltitudeM(Vehicle vehicle)
    {
        try
        {
            return IsReadable(vehicle) ? Sanitize.Finite(vehicle.GetBarometricAltitude()) : 0.0;
        }
        catch (Exception)
        {
            return 0.0;
        }
    }

    /// <summary>Speed relative to the rotating body frame, in m/s.</summary>
    /// <param name="vehicle">The vehicle.</param>
    /// <returns>The surface speed, 0 when unreadable.</returns>
    [KsaAnchor("Vehicle.GetSurfaceSpeed()",
        SourceFile = "KSA/Vehicle.cs:2759", Verified = "2026-08-07", GameVersion = "2026.8.5.5168",
        Risk = ChurnRisk.Low,
        Notes = "Do NOT use NavBallData.Speed (Vehicle.cs:575) — it is frame-dependent on the player's "
                + "navball mode (switch at Vehicle.cs:2506-2590).")]
    public static double SurfaceSpeedMs(Vehicle vehicle)
    {
        try
        {
            return IsReadable(vehicle) ? Sanitize.Finite(vehicle.GetSurfaceSpeed()) : 0.0;
        }
        catch (Exception)
        {
            return 0.0;
        }
    }

    /// <summary>
    /// The game's <c>LaunchGameTime</c> in sim seconds. Together with the vehicle id this is a
    /// flight's identity, so a save reload re-uses the same flight rather than minting a second.
    /// </summary>
    /// <param name="vehicle">The vehicle.</param>
    /// <returns>The launch time, <see cref="double.NaN"/> when unreadable.</returns>
    [KsaAnchor("Vehicle.LaunchGameTime (SimTime field)",
        SourceFile = "KSA/Vehicle.cs:162", Verified = "2026-08-07", GameVersion = "2026.8.5.5168",
        Risk = ChurnRisk.Low,
        Notes = "Set at ctor (:1313), restored from save (:922), inherited by split children (:1543) — all "
                + "three verified, which is what makes it a stable flight key.")]
    public static double LaunchGameTime(Vehicle vehicle)
    {
        try
        {
            return vehicle.LaunchGameTime.Seconds();
        }
        catch (Exception)
        {
            return double.NaN;
        }
    }

    /// <summary>Universe sim seconds. Never throws.</summary>
    /// <returns>The elapsed sim time in seconds.</returns>
    [KsaAnchor("Universe.GetElapsedSeconds()",
        SourceFile = "KSA/Universe.cs:2103", Verified = "2026-08-07", GameVersion = "2026.8.5.5168",
        Risk = ChurnRisk.Low)]
    public static double SimTimeSeconds()
    {
        try
        {
            return Sanitize.Finite(Universe.GetElapsedSeconds());
        }
        catch (Exception)
        {
            return 0.0;
        }
    }

    // ----- career identity (§4.1 `career`) -------------------------------------------------
    //
    // KSA ships no career, save or player identifier of any kind. Verified against the current
    // decomp (build 2026.8.5.5168), and recorded in docs/ksa-integration.md §5:
    //
    //   * the save root `UniverseData` has exactly four fields — GameTime, Camera,
    //     CelestialSystems, KittenRoster (KSA/UniverseData.cs:10-20) — and none of them is an id,
    //     a GUID, a creation stamp or a seed;
    //   * `Universe.DeserializeSave(UniverseData)` (KSA/Universe.cs:2140) receives no name, no
    //     path and no stream, so the mod's existing session-boundary patch cannot tell which save
    //     was loaded;
    //   * `SaveMetaData.Created` (KSA/SaveMetaData.cs:16-17) looks like an anchor and is not — a
    //     save overwrite is Delete-then-Make (KSA/UncompressedSave.cs:85-89) and the field
    //     initialiser re-stamps DateTime.UtcNow on every write.
    //
    // The one thing that *is* stable is the save's own name, `GameSave.Id` (KSA/GameSave.cs:13),
    // which is the folder under Documents/My Games/Kitten Space Agency/saves. It is reachable in
    // exactly two places, both patched by Patcher: the instance receiver of
    // `UncompressedSave.Load()` (KSA/UncompressedSave.cs:45) and the argument of the static
    // `UncompressedSave.Make(string)` (KSA/UncompressedSave.cs:104).

    private static string _careerKey = NewUnsavedCareerKey();

    /// <summary>
    /// The current career id — 16 Crockford characters, install-salted so the server never learns
    /// a save's name (§4.1).
    /// </summary>
    /// <param name="installId">The install ULID.</param>
    /// <returns>The career id.</returns>
    public static string CareerId(string installId) => Ids.CareerId(installId, _careerKey);

    /// <summary>
    /// Adopts the career of a KSA save. Called from the <c>UncompressedSave.Load</c> prefix (so the
    /// career is already correct when <c>Universe.DeserializeSave</c> raises the session boundary)
    /// and from the <c>UncompressedSave.Make</c> postfix (so a game that started unsaved joins the
    /// career of the slot it is first written to, and a "save as" carries the career with it).
    /// </summary>
    /// <param name="save">The save being loaded or written.</param>
    [KsaAnchor("GameSave.Id",
        SourceFile = "KSA/GameSave.cs:13 / KSA/UncompressedSave.cs:19",
        Verified = "2026-08-07", GameVersion = "2026.8.5.5168", Risk = ChurnRisk.Medium,
        Notes = "The save-folder name the player typed. The ONLY stable per-save identifier KSA has: "
                + "UniverseData carries no id/GUID/seed/creation stamp (KSA/UniverseData.cs:10-20) and "
                + "SaveMetaData.Created is re-stamped on every overwrite (KSA/UncompressedSave.cs:85-89). "
                + "Deleting a save and recreating it under the same name therefore re-uses the career id "
                + "— see docs/events.md for the stated limitation.")]
    public static void AdoptSaveCareer(GameSave? save)
    {
        try
        {
            string? id = save?.Id;
            if (!string.IsNullOrEmpty(id))
                _careerKey = "save:" + id;
        }
        catch (Exception ex)
        {
            // A career we cannot read is not worth a frame: keep the one we have and say so once.
            ModLog.Log.Warn($"catlog: could not read the loaded save's name: {ex.Message}");
        }
    }

    /// <summary>
    /// Starts a fresh, not-yet-saved career. Called from the <c>Universe.LoadSystem</c> postfix,
    /// which is the game's only new-game path (KSA/Universe.cs:167, sole caller KSA/Program.cs:965).
    /// </summary>
    /// <remarks>
    /// The key is random rather than a constant "unsaved" bucket on purpose: two fresh starts on
    /// one install are two different careers, and giving them one id would make the second one's
    /// clock look like the first one's clock running backwards.
    /// </remarks>
    public static void BeginUnsavedCareer() => _careerKey = NewUnsavedCareerKey();

    private static string NewUnsavedCareerKey() => "new:" + Ids.NewUlid();

    /// <summary>Every live vehicle in the current system, appended to <paramref name="into"/>.</summary>
    /// <remarks>
    /// Uses <c>UnsafeAsList()</c> + a type test rather than LINQ's <c>OfType&lt;Vehicle&gt;().ToList()</c>,
    /// which allocates a list and an enumerator on every single tick.
    /// </remarks>
    /// <param name="into">The list to append to. Cleared first.</param>
    [KsaAnchor("Universe.CurrentSystem.All.UnsafeAsList()",
        SourceFile = "KSA/Universe.cs:92 / KSA/CelestialSystem.cs:57 / KSA/LookupCollection.cs:210",
        Verified = "2026-08-07", GameVersion = "2026.8.5.5168", Risk = ChurnRisk.Low,
        Notes = "CurrentSystem is null before a system is loaded. Vehicle : Astronomical (Vehicle.cs:27), "
                + "KittenEva : Vehicle (KittenEva.cs:8), so the type test catches EVA kittens too.")]
    public static void CollectVehicles(List<Vehicle> into)
    {
        into.Clear();
        try
        {
            if (Universe.CurrentSystem is not { } system)
                return;

            foreach (Astronomical astronomical in system.All.UnsafeAsList())
            {
                if (astronomical is Vehicle vehicle && !vehicle.IsDisposed)
                    into.Add(vehicle);
            }
        }
        catch (Exception ex)
        {
            Faults.Note(ex);
        }
    }

    /// <summary>
    /// The live <c>KittenLocomotionTuning.Current.TumbleSpeedGate</c>. Compare against
    /// <see cref="StockTumbleSpeedGate"/>: any difference means the tumble classifier has been
    /// retuned and the session's tumble records are forgeable.
    /// </summary>
    /// <returns>The gate in m/s, or <see cref="StockTumbleSpeedGate"/> when unreadable.</returns>
    [KsaAnchor("KittenLocomotionTuning.Current.TumbleSpeedGate",
        SourceFile = "KSA/KittenLocomotionTuning.cs:33,59,77", Verified = "2026-08-07",
        GameVersion = "2026.8.5.5168", Risk = ChurnRisk.High,
        Notes = "NEW IN BUILD 5168. Current is a MUTABLE PUBLIC STATIC and KittenTuningWindow "
                + "(KittenTuningWindow.cs:9, instantiated Program.cs:3422) live-edits it by ref via "
                + "ImGui.DragFloat — ksa-integration B9. Stock is 6.5 (raised from 5.5 in r5131).")]
    public static float TumbleSpeedGate()
    {
        try
        {
            return KittenLocomotionTuning.Current.TumbleSpeedGate;
        }
        catch (Exception)
        {
            return StockTumbleSpeedGate;
        }
    }

    /// <summary>
    /// The locomotion mode of an EVA kitten, or <c>null</c> when the vehicle is not one.
    /// </summary>
    /// <param name="vehicle">The vehicle.</param>
    /// <returns>The mode, or null.</returns>
    [KsaAnchor("KittenEva.LocomotionState.Mode (LocomotionMode)",
        SourceFile = "KSA/KittenEva.cs:20 / KSA/LocomotionState.cs:5 / KSA/LocomotionMode.cs:3",
        Verified = "2026-08-07", GameVersion = "2026.8.5.5168", Risk = ChurnRisk.High,
        Notes = "NEW IN BUILD 5168 — the whole locomotion subsystem. LocomotionState is a get-only property "
                + "returning a STRUCT COPY, so no reflection and no aliasing. A tumble ends "
                + "Tumbling → Rightening → Grounded, so count transitions INTO Tumbling only; counting "
                + "transitions out would double-count via Rightening.")]
    public static LocomotionMode? LocomotionMode(Vehicle vehicle)
    {
        try
        {
            return vehicle is KittenEva eva ? eva.LocomotionState.Mode : null;
        }
        catch (Exception)
        {
            return null;
        }
    }

    /// <summary>
    /// The tangential ground speed the locomotion solver last computed for an EVA kitten, in m/s —
    /// the quantity the tumble gate is compared against.
    /// </summary>
    /// <param name="vehicle">The vehicle.</param>
    /// <returns>The ground speed, 0 when unavailable.</returns>
    [KsaAnchor("KittenEva.LocomotionState.GroundSpeed",
        SourceFile = "KSA/LocomotionState.cs:13", Verified = "2026-08-07", GameVersion = "2026.8.5.5168",
        Risk = ChurnRisk.High,
        Notes = "NEW IN BUILD 5168. The classifier itself uses LocomotionFacts.TangentialSpeedPhys "
                + "(KittenLocomotion.cs:30, computed VehicleUpdateTask.cs:1154), which is not exposed on the "
                + "state struct; GroundSpeed is the closest published quantity.")]
    public static double GroundSpeedMs(Vehicle vehicle)
    {
        try
        {
            return vehicle is KittenEva eva ? Sanitize.Finite(eva.LocomotionState.GroundSpeed) : 0.0;
        }
        catch (Exception)
        {
            return 0.0;
        }
    }

    /// <summary>Snapshots the whole kitten roster.</summary>
    /// <returns>One row per roster entry; empty when the roster cannot be read.</returns>
    [KsaAnchor("Universe.KittenRoster.Kittens (List<KittenRosterEntryData>); Name/TravelledMeters/FastestSpeed/"
               + "MissionCount/TotalMissionTime/Kia",
        SourceFile = "KSA/Universe.cs:94 / KSA/KittenRosterData.cs:13 / KSA/KittenRosterEntryData.cs:23-35",
        Verified = "2026-08-07", GameVersion = "2026.8.5.5168", Risk = ChurnRisk.Low,
        Notes = "TravelledMeters and FastestSpeed are DistanceReference CLASSES (.InMeters()); "
                + "TotalMissionTime is a SimTimeReference class (.GetSeconds()). The roster OBJECT is swapped "
                + "wholesale on save-load (Universe.cs:2178) and new game (:176) — ksa-integration B8 — so it "
                + "is re-resolved from Universe.KittenRoster on every call and never cached. "
                + "FastestSpeed is ECLIPTIC-FRAME (Vehicle.cs:2468): ~30 km/s baseline on Earth, recorded for "
                + "completeness only (docs/events.md).")]
    public static IReadOnlyList<RosterKitten> SampleRoster()
    {
        try
        {
            List<KittenRosterEntryData> kittens = Universe.KittenRoster.Kittens;
            var rows = new List<RosterKitten>(kittens.Count);
            foreach (KittenRosterEntryData kitten in kittens)
            {
                if (string.IsNullOrEmpty(kitten.Name))
                    continue;
                rows.Add(new RosterKitten(
                    Name: kitten.Name,
                    TravelledM: Sanitize.Finite(kitten.TravelledMeters.InMeters()),
                    FastestMs: Sanitize.Finite(kitten.FastestSpeed.InMeters()),
                    Missions: kitten.MissionCount,
                    MissionTimeS: Sanitize.Finite(kitten.TotalMissionTime.GetSeconds()),
                    Kia: kitten.Kia));
            }

            return rows;
        }
        catch (Exception ex)
        {
            Faults.Note(ex);
            return [];
        }
    }

    /// <summary>The vehicle's id, or an empty string when it cannot be read.</summary>
    /// <param name="vehicle">The vehicle.</param>
    /// <returns>The id.</returns>
    [KsaAnchor("Vehicle.Id (from Astronomical/IObjectId)",
        SourceFile = "KSA/Astronomical.cs:96", Verified = "2026-08-07", GameVersion = "2026.8.5.5168",
        Risk = ChurnRisk.Low,
        Notes = "KSA has no separate display name; the id doubles as vehicle_name (sanitized to 64 ASCII).")]
    public static string IdOf(Vehicle? vehicle)
    {
        try
        {
            return vehicle?.Id ?? string.Empty;
        }
        catch (Exception)
        {
            return string.Empty;
        }
    }

    /// <summary>True when the vehicle is an EVA kitten.</summary>
    /// <param name="vehicle">The vehicle.</param>
    /// <returns>True for a <c>KittenEva</c>.</returns>
    [KsaAnchor("KittenEva : Vehicle",
        SourceFile = "KSA/KittenEva.cs:8", Verified = "2026-08-07", GameVersion = "2026.8.5.5168",
        Risk = ChurnRisk.Low)]
    public static bool IsKitten(Vehicle? vehicle) => vehicle is KittenEva;

    /// <summary>True when any engine on the vehicle is active.</summary>
    /// <param name="vehicle">The vehicle.</param>
    /// <returns>True when at least one engine is active.</returns>
    [KsaAnchor("Vehicle.IsAnyEngineActive()",
        SourceFile = "KSA/Vehicle.cs:6030", Verified = "2026-08-07", GameVersion = "2026.8.5.5168",
        Risk = ChurnRisk.Medium,
        Notes = "Reads EngineControllerGlobalState.IsAnyActive off the vehicle's ModuleStateList — the whole "
                + "reason to use the game's own helper rather than walking Modules.Get<EngineController>() and "
                + "the parallel state span by hand.")]
    public static bool IsAnyEngineActive(Vehicle vehicle)
    {
        try
        {
            return vehicle.IsAnyEngineActive();
        }
        catch (Exception)
        {
            return false;
        }
    }

    /// <summary>
    /// True when any engine on the vehicle has propellant. Combined with
    /// <see cref="IsAnyEngineActive"/> this is the game's own flameout predicate.
    /// </summary>
    /// <remarks>
    /// <c>docs/ksa-integration.md</c> B3: <b>there is no flameout concept anywhere in the
    /// codebase</b> — zero hits for <c>flameout</c>, <c>starved</c> or <c>ResourceAvailable</c>. The
    /// game's own test is <c>IsActive &amp;&amp; !IsPropellantAvailable</c>
    /// (<c>EngineController.cs:60</c>), which is what these two helpers reconstruct at whole-vehicle
    /// granularity.
    /// </remarks>
    /// <param name="vehicle">The vehicle.</param>
    /// <returns>True when at least one engine has propellant available.</returns>
    [KsaAnchor("Vehicle.IsAnyEnginePropellantAvailable()",
        SourceFile = "KSA/Vehicle.cs:6131", Verified = "2026-08-07", GameVersion = "2026.8.5.5168",
        Risk = ChurnRisk.Medium)]
    public static bool IsAnyEnginePropellantAvailable(Vehicle vehicle)
    {
        try
        {
            return vehicle.IsAnyEnginePropellantAvailable();
        }
        catch (Exception)
        {
            return false;
        }
    }

    /// <summary>
    /// A description of the vehicle's currently active engines for the <c>engine.*</c> payload:
    /// the most common template id among them, and how many are active.
    /// </summary>
    /// <param name="vehicle">The vehicle.</param>
    /// <returns>The template name (empty when none) and the active count.</returns>
    [KsaAnchor("Vehicle.Parts.Modules.Get<EngineController>(); EngineController.IsActive; ModuleBase.TemplateId",
        SourceFile = "KSA/PartTree.cs:34 / KSA/EngineController.cs:36 / KSA/ModuleBase.cs:29",
        Verified = "2026-08-07", GameVersion = "2026.8.5.5168", Risk = ChurnRisk.Medium,
        Notes = "ModuleList.Get<T>() returns a Span<T> — check Length, never store it across frames. "
                + "EngineController.IsActive is a property with an internal setter; the per-engine "
                + "IsPropellantAvailable lives on the parallel EngineControllerState span, which PartTree "
                + "does NOT expose as a named StateList, hence the whole-vehicle globals above.")]
    public static (string Engine, int Count) ActiveEngines(Vehicle vehicle)
    {
        try
        {
            string engine = string.Empty;
            int count = 0;
            Span<EngineController> controllers = vehicle.Parts.Modules.Get<EngineController>();
            for (int i = 0; i < controllers.Length; i++)
            {
                if (!controllers[i].IsActive)
                    continue;
                count++;
                if (engine.Length == 0)
                    engine = controllers[i].TemplateId ?? string.Empty;
            }

            return (engine, count);
        }
        catch (Exception)
        {
            return (string.Empty, 0);
        }
    }

    /// <summary>Maps the game's destruction cause onto the wire enum.</summary>
    /// <param name="cause">The game cause.</param>
    /// <returns>The catlog cause.</returns>
    [KsaAnchor("VehicleDestructionCause (6 values)",
        SourceFile = "KSA/VehicleDestructionCause.cs:3-11", Verified = "2026-08-07",
        GameVersion = "2026.8.5.5168", Risk = ChurnRisk.Low,
        Notes = "Byte-identical to 5117. A NEW value added by a future build lands on GroundImpact here and "
                + "logs — the switch is deliberately total so an unknown cause cannot throw inside a patch.")]
    public static RudCause MapCause(VehicleDestructionCause cause) => cause switch
    {
        VehicleDestructionCause.GroundImpact => RudCause.GroundImpact,
        VehicleDestructionCause.OceanImpact => RudCause.OceanImpact,
        VehicleDestructionCause.Collision => RudCause.Collision,
        VehicleDestructionCause.ExcessiveGForce => RudCause.ExcessiveGForce,
        VehicleDestructionCause.AerodynamicForces => RudCause.AerodynamicForces,
        VehicleDestructionCause.HydrodynamicForces => RudCause.HydrodynamicForces,
        _ => UnknownCause(cause),
    };

    /// <summary>
    /// True when the game is currently suppressing impact FX for a vehicle — a five-second window
    /// after a teleport. Impacts inside it are not real lithobrakes.
    /// </summary>
    /// <param name="vehicle">The vehicle.</param>
    /// <returns>True when impacts should be ignored.</returns>
    [KsaAnchor("Vehicle.IsImpactFxSuppressed()",
        SourceFile = "KSA/Vehicle.cs:5271", Verified = "2026-08-07", GameVersion = "2026.8.5.5168",
        Risk = ChurnRisk.Low,
        Notes = "Program.GetPlayerTime() - _lastTeleportTime < 5.0. Note the game still APPLIES the impact "
                + "event when suppressed — only the FX are skipped — so a postfix on GroundImpactEvent.Apply "
                + "fires either way and must consult this itself.")]
    public static bool IsImpactSuppressed(Vehicle vehicle)
    {
        try
        {
            return vehicle.IsImpactFxSuppressed();
        }
        catch (Exception)
        {
            return false;
        }
    }

    /// <summary>The acceleration magnitude in g.</summary>
    /// <param name="vehicle">The vehicle.</param>
    /// <returns>The g-load, 0 when unreadable.</returns>
    [KsaAnchor("Vehicle.AccelerationBody (double3)",
        SourceFile = "KSA/Vehicle.cs:557", Verified = "2026-08-07", GameVersion = "2026.8.5.5168",
        Risk = ChurnRisk.Low)]
    public static double GLoad(Vehicle vehicle)
    {
        try
        {
            return Sanitize.Finite(vehicle.AccelerationBody.Length() / StandardGravity);
        }
        catch (Exception)
        {
            return 0.0;
        }
    }

    /// <summary>
    /// The lowercase name of a <c>Situation</c>. An exhaustive switch rather than
    /// <c>ToString().ToLowerInvariant()</c>: it allocates nothing at 2 Hz and it makes the open-set
    /// contract explicit — a value a future build adds becomes <c>"unknown"</c> rather than a
    /// mixed-case string the server has never seen.
    /// </summary>
    /// <param name="situation">The situation.</param>
    /// <returns>The lowercase name.</returns>
    [KsaAnchor("Situation (8 values)",
        SourceFile = "KSA/Situation.cs:3-13", Verified = "2026-08-07", GameVersion = "2026.8.5.5168",
        Risk = ChurnRisk.Low,
        Notes = "Byte-identical to 5117. The enum is a PACKED BITFIELD: value = (SurfaceContact << 1) | "
                + "onRailsBit (SituationEx.cs:54,60). catlog.lib's SituationInfo re-derives the contact and "
                + "rails bits from these names without referencing KSA, which is why the names must be exact.")]
    public static string SituationName(Situation situation) => situation switch
    {
        Situation.Maneuvering => "maneuvering",
        Situation.Freefall => "freefall",
        Situation.Rolling => "rolling",
        Situation.Landed => "landed",
        Situation.Sailing => "sailing",
        Situation.Floating => "floating",
        Situation.Dragging => "dragging",
        Situation.Bottomed => "bottomed",
        _ => "unknown",
    };

    private static RudCause UnknownCause(VehicleDestructionCause cause)
    {
        ModLog.Log.Warn(
            $"catlog: the game reported an unknown VehicleDestructionCause '{cause}'; recording it as "
            + "ground_impact. docs/ksa-integration.md §2 needs re-verifying against this build.");
        return RudCause.GroundImpact;
    }

    /// <summary>
    /// The log-once latch for per-vehicle read faults. A vehicle mid-teardown throwing on every
    /// frame must not produce a per-frame log line, and the whole sample pass must survive it.
    /// </summary>
    internal static class Faults
    {
        private static bool _logged;

        /// <summary>How many read faults have been swallowed this session (status window).</summary>
        internal static long Count { get; private set; }

        internal static void Note(Exception ex)
        {
            Count++;
            if (_logged)
                return;
            _logged = true;
            ModLog.Log.Debug($"catlog: a KSA read failed (logged once this session): {ex.Message}");
        }
    }
}
