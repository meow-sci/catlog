using System;
using System.Collections.Generic;
using System.IO;
using System.Linq;
using MeowSci.Catlog.Lib.Config;
using MeowSci.Catlog.Lib.Detect;
using MeowSci.Catlog.Lib.Events;
using Xunit;

namespace MeowSci.Catlog.Lib.Tests.Config;

/// <summary>
/// The five event types no configuration can switch off: <c>session.started</c>,
/// <c>flight.started</c>, <c>flight.ended</c>, <c>flight.flagged</c> and <c>kitten.kia</c>.
/// </summary>
/// <remarks>
/// <para>
/// <b>The threat this closes.</b> Same model as the 30-second reporting interval, in a different
/// key: <c>catlog.toml</c> is a text file in the player's own mod folder, and the attacker is a
/// player editing it. <c>flight.flagged</c> is the only thing that ever marks a run as tainted —
/// the server's flag bits have exactly one writer — so <c>"flight.flagged" = false</c> would be a
/// one-line edit that makes teleporting, refuelling, resource editing and console commands score
/// normally and show up on the public pages. <c>kitten.kia</c> is the one that stops a crash which
/// killed the crew being counted as a lithobrake survived. The other three make a flight a flight
/// and a reload a reload. Recompiling the assembly can still do anything and always will be able
/// to; that is explicitly not what this defends against. The easy path is shut.
/// </para>
/// <para>
/// Two layers, because clamping the config alone only closes the path you thought of:
/// <see cref="ModConfig.Normalize"/> drops the key with a warning so the file the player reads is
/// the truth, and <see cref="EventTypeFilter"/> refuses it again so an
/// <see cref="EventPipelineOptions"/> built by hand cannot express it either. Only the second is
/// the guarantee.
/// </para>
/// </remarks>
public sealed class EventGateTests
{
    /// <summary>
    /// The list is exactly five names, and it is these five. This test exists so that shortening it
    /// is a deliberate act with a failing test attached, not a quiet edit.
    /// </summary>
    [Fact]
    public void TheLockedListIsExactlyTheIntegritySpine()
    {
        Assert.Equal(
            ["flight.ended", "flight.flagged", "flight.started", "kitten.kia", "session.started"],
            EventTypes.AlwaysReported.OrderBy(static t => t, StringComparer.Ordinal).ToArray());

        // Every one of them is a real registered type, not a typo that would never match.
        foreach (string type in EventTypes.AlwaysReported)
            Assert.True(EventTypes.IsKnown(type), $"{type} is not in the registry");
    }

    /// <summary>
    /// The headline case, end to end from a real TOML file: a player writes the whole spine off and
    /// the mod reports all of it anyway, having said so in the log.
    /// </summary>
    [Fact]
    public void NoTomlKeyCanTurnOffTheSpine()
    {
        using var dir = new TempDir();
        string path = dir.File("catlog.toml");
        File.WriteAllText(
            path,
            """
            schema = 1

            [events]
            "flight.flagged" = false
            "kitten.kia" = false
            "session.started" = false
            "flight.started" = false
            "flight.ended" = false
            "telemetry.window" = false
            """);

        ModConfig config = ModConfig.LoadOrCreate(path);

        // The one legitimate opt-out in that file is the only one that survived.
        Assert.Equal([EventTypes.TelemetryWindow], config.Events.Keys.ToArray());
        foreach (string type in EventTypes.AlwaysReported)
        {
            Assert.False(config.Events.ContainsKey(type), $"{type} should have been dropped");
            Assert.True(config.EventFilter().IsEnabled(type));
        }

        // The rewritten file cannot carry such a key either: Normalize dropped it before Save saw
        // it, so there is nothing to write. The commented example block says `= true` throughout.
        string written = config.Serialize();
        foreach (string type in EventTypes.AlwaysReported)
            Assert.DoesNotContain($"\"{type}\" = false", written, StringComparison.Ordinal);

        // And the file the player reads says so in as many words.
        Assert.Contains("Five types cannot be switched off", written, StringComparison.Ordinal);
    }

    [Theory]
    [InlineData("session.started")]
    [InlineData("flight.started")]
    [InlineData("flight.ended")]
    [InlineData("flight.flagged")]
    [InlineData("kitten.kia")]
    public void NormalizeDropsALockedFalse(string type)
    {
        var config = new ModConfig { Events = { [type] = false } };

        config.Normalize();

        Assert.Empty(config.Events);
        Assert.Same(EventTypeFilter.All, config.EventFilter());
    }

    /// <summary>
    /// Defence in depth: the config clamp is a courtesy. This is the guarantee — a filter built by
    /// hand, bypassing <see cref="ModConfig"/> entirely, naming every locked type explicitly, still
    /// reports all of them.
    /// </summary>
    [Fact]
    public void ADirectlyConstructedFilterCannotSuppressTheSpine()
    {
        EventTypeFilter filter = EventTypeFilter.Create([.. EventTypes.AlwaysReported, EventTypes.VehicleRud]);

        foreach (string type in EventTypes.AlwaysReported)
            Assert.True(filter.IsEnabled(type), $"{type} must always be reported");

        Assert.False(filter.IsEnabled(EventTypes.VehicleRud));
        Assert.Equal([EventTypes.VehicleRud], filter.Disabled.ToArray());
    }

    /// <summary>
    /// And the same at the choke point itself: a pipeline handed a hostile filter still produces
    /// the flag, the KIA, the start and the end.
    /// </summary>
    [Fact]
    public void APipelineWithAHostileFilterStillEmitsTheSpine()
    {
        EventPipeline pipeline = TestData.Pipeline(
            types: EventTypeFilter.Create([.. EventTypes.AlwaysReported]));
        var produced = new List<EventEnvelope>();

        produced.Add(pipeline.SessionStarted(0, TestData.WallMs));
        produced.AddRange(pipeline.ProcessSignal(TestData.Created(simT: 0, crewCount: 2)));
        produced.AddRange(pipeline.ProcessSignal(
            new FlaggedSignal(5, TestData.WallMs, "v1", FlightFlag.Teleport, "Vehicle.Teleport")));
        produced.AddRange(pipeline.ProcessSignal(
            new KiaSignal(6, TestData.WallMs, "Whiskers", KiaContext.Rud)));
        produced.AddRange(pipeline.ProcessSignal(
            new VehicleRemovedSignal(7, TestData.WallMs, "v1", FlightEndReason.Destroyed, 0)));

        Assert.Equal(
            [
                EventTypes.SessionStarted,
                EventTypes.FlightStarted,
                EventTypes.FlightFlagged,
                EventTypes.KittenKia,
                EventTypes.FlightEnded,
            ],
            produced.Select(static e => e.Type).ToArray());
    }

    /// <summary>
    /// The <c>[events]</c> table names types, and nothing else. A file that tries to name the
    /// mechanism — a master off switch for flagging, a "clean run" mode — is simply ignored: those
    /// are not event types, and the mechanism is not a key to begin with.
    /// </summary>
    [Fact]
    public void NoTomlKeyCanNameTheGateItself()
    {
        using var dir = new TempDir();
        string path = dir.File("catlog.toml");
        File.WriteAllText(
            path,
            """
            schema = 1
            disable_flagging = true
            report_flags = false
            always_reported = []
            integrity = false

            [events]
            "flight.flagged " = false
            "FLIGHT.FLAGGED" = false
            "flags" = false
            "*" = false
            """);

        ModConfig config = ModConfig.LoadOrCreate(path);

        Assert.Empty(config.Events);
        Assert.Same(EventTypeFilter.All, config.EventFilter());
        Assert.True(config.EventFilter().IsEnabled(EventTypes.FlightFlagged));

        string written = config.Serialize();
        Assert.DoesNotContain("disable_flagging", written, StringComparison.OrdinalIgnoreCase);
        Assert.DoesNotContain("report_flags", written, StringComparison.OrdinalIgnoreCase);
        Assert.DoesNotContain("always_reported", written, StringComparison.OrdinalIgnoreCase);
    }
}
