using System.Linq;
using System.Text.RegularExpressions;
using MeowSci.Catlog.Lib.Events;
using Xunit;

namespace MeowSci.Catlog.Lib.Tests.Events;

/// <summary>§4.2: the launch-set registry, and the outbox kind classification.</summary>
public sealed partial class EventTypesTests
{
    [Fact]
    public void RegistryHasExactlyTheLaunchSet()
    {
        // 23 rows: the §4.2 table plus vehicle.landed, counting each of the docked/undocked and
        // engine.* variants.
        Assert.Equal(23, EventTypes.All.Count);

        // Every type is at ver 1: there is one shape of every event. The server's
        // projector.currentVer is correspondingly empty. A bump here is half a change — the
        // other half is an upcaster on the server for every version in between — so the whole
        // set is pinned rather than left implicit.
        Assert.All(EventTypes.All, static type => Assert.Equal(1, EventTypes.VersionOf(type)));
    }

    [Fact]
    public void EveryTypeMatchesTheEnvelopeGrammar()
    {
        // §4.1: namespaced, lowercase, [a-z0-9_.]
        Assert.All(EventTypes.All, type => Assert.Matches(TypeGrammar(), type));
    }

    [Fact]
    public void UnknownTypesAreNotInTheRegistry()
    {
        Assert.False(EventTypes.IsKnown("vehicle.exploded"));
        Assert.False(EventTypes.IsKnown(null));
        Assert.True(EventTypes.IsKnown(EventTypes.VehicleRud));
    }

    /// <summary>
    /// Prune drops passive rows first and never drops scoring rows, so this classification decides
    /// what survives a full outbox. Only <c>telemetry.window</c> is droppable.
    /// </summary>
    [Fact]
    public void OnlyTelemetryWindowIsPassive()
    {
        string[] passive = EventTypes.All.Where(static t => EventTypes.KindOf(t) == EventTypes.KindPassive).ToArray();

        Assert.Equal([EventTypes.TelemetryWindow], passive);
    }

    [Theory]
    [InlineData(RudCause.GroundImpact, "ground_impact")]
    [InlineData(RudCause.OceanImpact, "ocean_impact")]
    [InlineData(RudCause.Collision, "collision")]
    [InlineData(RudCause.ExcessiveGForce, "excessive_g_force")]
    [InlineData(RudCause.AerodynamicForces, "aerodynamic_forces")]
    [InlineData(RudCause.HydrodynamicForces, "hydrodynamic_forces")]
    public void RudCausesMatchTheContract(RudCause cause, string expected)
    {
        Assert.Equal(expected, EventTypes.ToWire(cause));
    }

    [Theory]
    [InlineData(FlightEndReason.Recovered, "recovered")]
    [InlineData(FlightEndReason.Destroyed, "destroyed")]
    [InlineData(FlightEndReason.Despawned, "despawned")]
    public void FlightEndReasonsMatchTheContract(FlightEndReason reason, string expected)
    {
        Assert.Equal(expected, EventTypes.ToWire(reason));
    }

    /// <summary>
    /// <c>tuning</c> is the amendment recorded in DECISIONS 2026-08-06: the game ships a debug
    /// window that live-edits the tumble speed gate.
    /// </summary>
    [Theory]
    [InlineData(FlightFlag.Teleport, "teleport")]
    [InlineData(FlightFlag.Refuel, "refuel")]
    [InlineData(FlightFlag.ResourceEdit, "resource_edit")]
    [InlineData(FlightFlag.Console, "console")]
    [InlineData(FlightFlag.Tuning, "tuning")]
    public void FlightFlagsMatchTheContract(FlightFlag flag, string expected)
    {
        Assert.Equal(expected, EventTypes.ToWire(flag));
    }

    [Theory]
    [InlineData(KiaContext.Rud, "rud")]
    [InlineData(KiaContext.ManualDestroy, "manual_destroy")]
    [InlineData(KiaContext.Unknown, "unknown")]
    public void KiaContextsMatchTheContract(KiaContext context, string expected)
    {
        Assert.Equal(expected, EventTypes.ToWire(context));
    }

    [GeneratedRegex("^[a-z0-9_]+\\.[a-z0-9_]+$")]
    private static partial Regex TypeGrammar();
}
