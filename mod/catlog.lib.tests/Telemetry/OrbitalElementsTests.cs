using MeowSci.Catlog.Lib.Telemetry;
using Xunit;

namespace MeowSci.Catlog.Lib.Tests.Telemetry;

public sealed class OrbitalElementsTests
{
    [Fact]
    public void SnapshotInitOnlyPropertiesCarryTheFinalOrbitalElements()
    {
        TelemetrySnapshot snapshot = TestData.Snapshot(
            smaM: 6_557_100.375,
            lanDeg: 72.25,
            argpDeg: 14.75,
            tPe: 160.125,
            periodS: 5_420.5);

        Assert.Equal(6_557_100.375, snapshot.SmaM);
        Assert.Equal(72.25, snapshot.LanDeg);
        Assert.Equal(14.75, snapshot.ArgpDeg);
        Assert.Equal(160.125, snapshot.TPe);
        Assert.Equal(5_420.5, snapshot.PeriodS);
    }
}
