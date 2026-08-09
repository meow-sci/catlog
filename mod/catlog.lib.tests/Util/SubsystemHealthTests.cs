using System;
using System.Collections.Generic;
using MeowSci.Catlog.Lib.Util;
using Xunit;

namespace MeowSci.Catlog.Lib.Tests.Util;

/// <summary>
/// §7.2: the per-subsystem dead latch — one ERROR log, then that subsystem is off
/// for the session and visible in the status window.
/// </summary>
public sealed class SubsystemHealthTests
{
    [Fact]
    public void FaultLatchesAndTheFirstReasonWins()
    {
        var health = new SubsystemHealth();

        health.Fault("outbox", "disk full");
        health.Fault("outbox", "something else entirely");

        Assert.True(health.IsDead("outbox"));
        SubsystemFault fault = Assert.Single(health.Snapshot());
        Assert.Equal("outbox", fault.Subsystem);
        Assert.Equal("disk full", fault.Error);
    }

    [Fact]
    public void UnfaultedSubsystemsAreAlive()
    {
        var health = new SubsystemHealth();

        Assert.False(health.IsDead("shipper"));
        Assert.Empty(health.Snapshot());
    }

    [Fact]
    public void SubsystemsAreIndependent()
    {
        var health = new SubsystemHealth();

        health.Fault("shipper", "license revoked");

        Assert.True(health.IsDead("shipper"));
        Assert.False(health.IsDead("detector"));
    }

    /// <summary>
    /// A transient telemetry read can recover; a bad credential or an unopenable outbox cannot, so
    /// those latch permanently and <see cref="SubsystemHealth.Clear"/> must not un-latch them.
    /// </summary>
    [Fact]
    public void PermanentLatchesDoNotClearButTransientOnesDo()
    {
        var health = new SubsystemHealth();
        health.Fault("credential", "jkt mismatch");
        health.Fault("telemetry", "a vehicle read threw", permanent: false);

        health.Clear("credential");
        health.Clear("telemetry");

        Assert.True(health.IsDead("credential"), "a config/credential fault never self-heals");
        Assert.False(health.IsDead("telemetry"), "a transient read fault clears on the next success");
    }

    [Fact]
    public void ResetDropsEveryLatch()
    {
        var health = new SubsystemHealth();
        health.Fault("outbox", "boom");
        health.Fault("shipper", "bang");

        health.Reset();

        Assert.Empty(health.Snapshot());
        Assert.False(health.IsDead("outbox"));
    }

    /// <summary>
    /// The latch table is copy-on-write so the game thread's reads take no lock, and the property
    /// that buys is exactly this: a snapshot handed out is a value, not a live view. A reader
    /// iterating it while a background task faults must not see the collection change underneath.
    /// </summary>
    [Fact]
    public void ASnapshotIsUnaffectedByLaterFaultsAndClears()
    {
        var health = new SubsystemHealth();
        health.Fault("outbox", "disk full");

        IReadOnlyList<SubsystemFault> taken = health.Snapshot();
        health.Fault("shipper", "license revoked");
        health.Reset();

        Assert.Single(taken);
        Assert.Equal("outbox", taken[0].Subsystem);
        Assert.Empty(health.Snapshot());
    }

    [Fact]
    public void FaultCarriesTheException()
    {
        var health = new SubsystemHealth();

        health.Fault("outbox", "open failed", new InvalidOperationException("nope"));

        IReadOnlyList<SubsystemFault> snapshot = health.Snapshot();
        Assert.Single(snapshot);
        Assert.True(snapshot[0].Permanent);
    }
}
