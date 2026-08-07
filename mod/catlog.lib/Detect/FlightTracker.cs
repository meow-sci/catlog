using System;
using System.Collections.Generic;
using MeowSci.Catlog.Lib.Events;
using MeowSci.Catlog.Lib.Util;

namespace MeowSci.Catlog.Lib.Detect;

/// <summary>
/// Owns the identifiers every envelope needs: the career id, the session ULID, the per-vehicle
/// flight ULID, and the set of integrity flags raised against each flight.
/// </summary>
/// <remarks>
/// <para>
/// A flight is identified by <c>(vehicle_id, launch_game_time)</c>. The game's
/// <c>Vehicle.LaunchGameTime</c> is set at construction and survives a save load
/// (<c>docs/ksa-integration.md</c> §1), so reloading a save re-uses the same flight rather than
/// minting a second one, while a genuinely new vehicle with a recycled id gets a fresh flight.
/// </para>
/// <para>
/// A <b>career</b> is one KSA save played over time. It outlives a session: a session is minted at
/// every save-load boundary, so one career is many sessions, and <c>sim_t</c> is only comparable
/// within a career (§4.1). The career changes only when <see cref="NewSession"/> is told it has.
/// </para>
/// <para>
/// Flags are accumulated per flight and deduped: a teleport detected on twenty consecutive frames
/// emits exactly one <c>flight.flagged</c>. The server's projector excludes flagged flights from
/// the boards, so one flag is all it takes.
/// </para>
/// </remarks>
public sealed class FlightTracker
{
    private readonly Dictionary<string, FlightRecord> _active = new(StringComparer.Ordinal);

    /// <summary>Creates a tracker.</summary>
    /// <param name="installId">The install ULID; salts every <c>kid</c> (§4.2) and every career id.</param>
    /// <param name="sessionId">The session ULID; a fresh one is minted when null.</param>
    /// <param name="careerId">
    /// The career id. When null, a per-install career id is derived — the right answer for any
    /// caller that has no concept of a save (the load harness, the conformance tests), because
    /// such a caller has exactly one career.
    /// </param>
    public FlightTracker(string installId, string? sessionId = null, string? careerId = null)
    {
        InstallId = installId;
        SessionId = sessionId ?? Ids.NewUlid();
        CareerId = careerId ?? Ids.CareerId(installId, "install");
    }

    /// <summary>The install ULID, stable across sessions on one machine.</summary>
    public string InstallId { get; }

    /// <summary>The current session ULID. Never null.</summary>
    public string SessionId { get; private set; }

    /// <summary>The current career id — 16 Crockford characters, never null (§4.1).</summary>
    public string CareerId { get; private set; }

    /// <summary>Vehicle ids with an open flight.</summary>
    public IReadOnlyCollection<string> ActiveVehicleIds => _active.Keys;

    /// <summary>
    /// Starts a new session: mints a session ULID and drops every open flight. Called on a save
    /// load, which is a hard teardown-and-rebuild boundary in the game.
    /// </summary>
    /// <param name="careerId">
    /// The career the new session belongs to, or null to stay in the current one. Null is the
    /// honest answer when the mod could not tell which save was loaded — the boundary still ends
    /// the session, it just does not claim the career changed.
    /// </param>
    /// <returns>The new session ULID.</returns>
    public string NewSession(string? careerId = null)
    {
        _active.Clear();
        SessionId = Ids.NewUlid();
        if (!string.IsNullOrEmpty(careerId))
            CareerId = careerId;
        return SessionId;
    }

    /// <summary>
    /// The flight ULID for a vehicle, minting one on first sight. Passing a
    /// <paramref name="launchGameTime"/> that differs from the open flight's retires that flight
    /// and starts a new one.
    /// </summary>
    /// <param name="vehicleId">The vehicle id.</param>
    /// <param name="launchGameTime">
    /// The game's <c>LaunchGameTime</c> for this vehicle, or <see cref="double.NaN"/> when the
    /// caller does not know it (a vehicle first seen through telemetry rather than through a
    /// creation signal — e.g. everything that already exists when a save is loaded).
    /// </param>
    /// <returns>The flight ULID.</returns>
    public string FlightFor(string vehicleId, double launchGameTime = double.NaN)
    {
        if (_active.TryGetValue(vehicleId, out FlightRecord? existing))
        {
            bool sameFlight = double.IsNaN(launchGameTime)
                              || double.IsNaN(existing.LaunchGameTime)
                              || existing.LaunchGameTime.Equals(launchGameTime);
            if (sameFlight)
            {
                // Learn the launch time if we did not have it before.
                if (double.IsNaN(existing.LaunchGameTime) && !double.IsNaN(launchGameTime))
                    existing.LaunchGameTime = launchGameTime;
                return existing.FlightId;
            }
        }

        var record = new FlightRecord(Ids.NewUlid(), launchGameTime);
        _active[vehicleId] = record;
        return record.FlightId;
    }

    /// <summary>The flight ULID for a vehicle, without minting one.</summary>
    /// <param name="vehicleId">The vehicle id.</param>
    /// <returns>The flight ULID, or null when the vehicle has no open flight.</returns>
    public string? PeekFlight(string vehicleId)
        => _active.TryGetValue(vehicleId, out FlightRecord? record) ? record.FlightId : null;

    /// <summary>Closes a vehicle's flight.</summary>
    /// <param name="vehicleId">The vehicle id.</param>
    /// <returns>The flight ULID that was closed, or null when there was none.</returns>
    public string? EndFlight(string vehicleId)
        => _active.Remove(vehicleId, out FlightRecord? record) ? record.FlightId : null;

    /// <summary>
    /// Raises an integrity flag against a vehicle's flight, deduping per (flight, flag).
    /// </summary>
    /// <param name="vehicleId">The vehicle id.</param>
    /// <param name="flag">The flag.</param>
    /// <returns>True when this is the first time the flag has been raised on the current flight.</returns>
    public bool AddFlag(string vehicleId, FlightFlag flag)
    {
        FlightRecord record = _active.TryGetValue(vehicleId, out FlightRecord? existing)
            ? existing
            : Register(vehicleId);
        return record.Flags.Add(flag);
    }

    /// <summary>The flags raised against a vehicle's current flight.</summary>
    /// <param name="vehicleId">The vehicle id.</param>
    /// <returns>The flag set, empty when the vehicle has no open flight.</returns>
    public IReadOnlyCollection<FlightFlag> FlagsFor(string vehicleId)
        => _active.TryGetValue(vehicleId, out FlightRecord? record)
            ? record.Flags
            : Array.Empty<FlightFlag>();

    /// <summary>True when a flag has already been raised on this vehicle's current flight.</summary>
    /// <param name="vehicleId">The vehicle id.</param>
    /// <param name="flag">The flag.</param>
    /// <returns>True when the flag is set.</returns>
    public bool HasFlag(string vehicleId, FlightFlag flag)
        => _active.TryGetValue(vehicleId, out FlightRecord? record) && record.Flags.Contains(flag);

    private FlightRecord Register(string vehicleId)
    {
        var record = new FlightRecord(Ids.NewUlid(), double.NaN);
        _active[vehicleId] = record;
        return record;
    }

    private sealed class FlightRecord(string flightId, double launchGameTime)
    {
        internal string FlightId { get; } = flightId;

        internal double LaunchGameTime { get; set; } = launchGameTime;

        internal HashSet<FlightFlag> Flags { get; } = [];
    }
}
