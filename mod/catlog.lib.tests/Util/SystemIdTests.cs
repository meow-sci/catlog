using System;
using System.Collections.Generic;
using System.Globalization;
using System.Text;
using MeowSci.Catlog.Lib.Telemetry;
using MeowSci.Catlog.Lib.Util;
using Xunit;

namespace MeowSci.Catlog.Lib.Tests.Util;

public sealed class SystemIdTests
{
    [Fact]
    public void CompleteKnownVector_IsPinned()
    {
        Assert.Equal("v93yzxjgz4abrnv0", Ids.SystemId(CompleteInput()));
    }

    [Fact]
    public void BodyInputOrder_DoesNotChangeIdentity()
    {
        SystemHashInput input = CompleteInput();
        var reversed = new List<SystemBodyHashInput>(input.Bodies);
        reversed.Reverse();

        Assert.Equal(Ids.SystemId(input), Ids.SystemId(input with { Bodies = reversed.AsReadOnly() }));
    }

    [Fact]
    public void Culture_DoesNotChangeIdentity()
    {
        CultureInfo before = CultureInfo.CurrentCulture;
        try
        {
            string expected = Ids.SystemId(CompleteInput());
            CultureInfo.CurrentCulture = CultureInfo.GetCultureInfo("fr-FR");
            Assert.Equal(expected, Ids.SystemId(CompleteInput()));
        }
        finally
        {
            CultureInfo.CurrentCulture = before;
        }
    }

    [Fact]
    public void NegativeZero_IsCanonicalPositiveZero()
    {
        SystemHashInput positive = SingleValue(0.0);
        SystemHashInput negative = SingleValue(-0.0);
        Assert.Equal(Ids.SystemId(positive), Ids.SystemId(negative));
    }

    [Fact]
    public void NonFiniteTags_CanonicalizeNaNsButDistinguishAllThreeClasses()
    {
        double payloadNaN = BitConverter.Int64BitsToDouble(unchecked((long)0x7ff8000000000123));
        string nan = Ids.SystemId(SingleValue(double.NaN));
        Assert.Equal(nan, Ids.SystemId(SingleValue(payloadNaN)));
        Assert.NotEqual(nan, Ids.SystemId(SingleValue(double.PositiveInfinity)));
        Assert.NotEqual(nan, Ids.SystemId(SingleValue(double.NegativeInfinity)));
        Assert.NotEqual(
            Ids.SystemId(SingleValue(double.PositiveInfinity)),
            Ids.SystemId(SingleValue(double.NegativeInfinity)));
    }

    [Fact]
    public void LengthPrefixes_DisambiguateSeparatorBearingIds()
    {
        SystemHashInput separators = CompleteInput() with
        {
            SystemId = "Sol\tDense\nMod",
            DisplayName = "A:B|C",
            HomeBodyId = "Earth\nMoon",
        };
        SystemHashInput redistributed = separators with
        {
            SystemId = "Sol",
            DisplayName = "Dense\nMod\tA:B|C",
        };
        Assert.NotEqual(Ids.SystemId(separators), Ids.SystemId(redistributed));
    }

    [Fact]
    public void StrictUtf8_RejectsUnpairedSurrogates()
    {
        Assert.Throws<EncoderFallbackException>(() => Ids.SystemId(CompleteInput() with { SystemId = "bad\ud800" }));
    }

    private static SystemHashInput SingleValue(double radius)
    {
        SystemBodyHashInput body = CompleteInput().Bodies[0] with { RadiusM = radius };
        return new SystemHashInput("one", "One", "root", 1, new[] { body });
    }

    private static SystemHashInput CompleteInput()
    {
        var root = new SystemBodyHashInput(
            "Root\nStar", null, "StellarBody", "star", 0,
            696_340_000, 1.98847e30, double.PositiveInfinity, 0, 0, 0,
            new Vec3(0, 0, 1), new Quat(0, 0, 0, 1), null, null);
        var world = new SystemBodyHashInput(
            "Éarth:Prime", "Root\nStar", "AtmosphericBody", "planet", 1,
            6_371_000, 5.9722e24, 924_000_000, 100_000, 0, 7.2921159e-5,
            new Vec3(0.1, -0.2, 0.9746794344808963),
            Quat.Canonical(0.1, 0.2, 0.3, 0.9),
            new SystemOrbitHashInput(149_597_870_700, 0.0167086, 0.00005, -11.26064, 114.20783, -1234.5),
            31_558_149.7635456);
        return new SystemHashInput(
            "Sol|Mod", "Système solaire", "Éarth:Prime", 2,
            new[] { world, root });
    }
}
