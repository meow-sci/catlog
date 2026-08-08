using MeowSci.Catlog.Lib.Detect;
using MeowSci.Catlog.Lib.Events;
using MeowSci.Catlog.Lib.Util;
using Xunit;

namespace MeowSci.Catlog.Lib.Tests.Detect;

/// <summary>
/// §7.2: <c>(vehicle_id, launch_game_time)</c> → flight ULID, session ULID, and
/// per-flight flag accumulation.
/// </summary>
public sealed class FlightTrackerTests
{
    [Fact]
    public void SessionId_IsAUlidAndStableUntilRotated()
    {
        var tracker = new FlightTracker(TestData.InstallId);

        Assert.True(Ids.IsUlid(tracker.SessionId), $"'{tracker.SessionId}' should be a ULID");
        Assert.Equal(tracker.SessionId, tracker.SessionId);
    }

    [Fact]
    public void FlightFor_MintsOnceAndThenReturnsTheSameId()
    {
        var tracker = new FlightTracker(TestData.InstallId);

        string first = tracker.FlightFor("v1", 100.0);
        string second = tracker.FlightFor("v1", 100.0);

        Assert.Equal(first, second);
        Assert.True(Ids.IsUlid(first));
    }

    /// <summary>
    /// The game's <c>LaunchGameTime</c> survives a save load, so re-registering the same vehicle
    /// must not mint a second flight; a genuinely new vehicle reusing the id must.
    /// </summary>
    [Fact]
    public void DifferentLaunchGameTime_StartsANewFlight()
    {
        var tracker = new FlightTracker(TestData.InstallId);
        string first = tracker.FlightFor("v1", 100.0);

        string second = tracker.FlightFor("v1", 900.0);

        Assert.NotEqual(first, second);
    }

    [Fact]
    public void UnknownLaunchGameTime_AdoptsTheOpenFlightAndLearnsTheTime()
    {
        var tracker = new FlightTracker(TestData.InstallId);
        string viaTelemetry = tracker.FlightFor("v1"); // first seen through a snapshot, no launch time

        string viaCreation = tracker.FlightFor("v1", 42.0); // the creation signal arrives later

        Assert.Equal(viaTelemetry, viaCreation);
        Assert.Equal(viaCreation, tracker.FlightFor("v1", 42.0));
    }

    [Fact]
    public void PeekFlight_DoesNotMint()
    {
        var tracker = new FlightTracker(TestData.InstallId);

        Assert.Null(tracker.PeekFlight("v1"));
        Assert.Empty(tracker.ActiveVehicleIds);
    }

    [Fact]
    public void EndFlight_ClosesAndForgets()
    {
        var tracker = new FlightTracker(TestData.InstallId);
        string flight = tracker.FlightFor("v1", 1.0);

        Assert.Equal(flight, tracker.EndFlight("v1"));
        Assert.Null(tracker.PeekFlight("v1"));
        Assert.Null(tracker.EndFlight("v1"));
        Assert.NotEqual(flight, tracker.FlightFor("v1", 1.0));
    }

    [Fact]
    public void Flags_AreDedupedPerFlight()
    {
        var tracker = new FlightTracker(TestData.InstallId);
        tracker.FlightFor("v1", 1.0);

        Assert.True(tracker.AddFlag("v1", FlightFlag.Teleport), "first raise wins");
        Assert.False(tracker.AddFlag("v1", FlightFlag.Teleport), "a teleport detected every frame emits once");
        Assert.True(tracker.AddFlag("v1", FlightFlag.Tuning), "a different flag is a different event");
        Assert.True(tracker.HasFlag("v1", FlightFlag.Teleport));
        Assert.Equal(2, tracker.FlagsFor("v1").Count);
    }

    [Fact]
    public void Flags_DoNotSurviveIntoTheNextFlight()
    {
        var tracker = new FlightTracker(TestData.InstallId);
        tracker.FlightFor("v1", 1.0);
        tracker.AddFlag("v1", FlightFlag.Refuel);
        tracker.EndFlight("v1");

        tracker.FlightFor("v1", 2.0);

        Assert.False(tracker.HasFlag("v1", FlightFlag.Refuel));
    }

    [Fact]
    public void NewSession_RotatesTheIdAndDropsEveryFlight()
    {
        var tracker = new FlightTracker(TestData.InstallId);
        string oldSession = tracker.SessionId;
        tracker.FlightFor("v1", 1.0);

        string newSession = tracker.NewSession();

        Assert.NotEqual(oldSession, newSession);
        Assert.Equal(newSession, tracker.SessionId);
        Assert.Empty(tracker.ActiveVehicleIds);
    }

    [Fact]
    public void AddFlag_OnAnUnknownVehicle_RegistersAFlight()
    {
        var tracker = new FlightTracker(TestData.InstallId);

        Assert.True(tracker.AddFlag("ghost", FlightFlag.Console));
        Assert.NotNull(tracker.PeekFlight("ghost"));
    }
}
