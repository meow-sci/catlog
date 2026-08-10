using System.Collections.Generic;
using System.Linq;
using MeowSci.Catlog.Lib.Detect;
using MeowSci.Catlog.Lib.Events;
using MeowSci.Catlog.Lib.Telemetry;
using Xunit;

namespace MeowSci.Catlog.Lib.Tests.Detect;

/// <summary>
/// §7.2 detector rules: situation diff, atmosphere hysteresis (±2%), orbit
/// achieved (<c>ecc &lt; 1 &amp;&amp; pe_alt &gt; atmo_height + 1000</c>), orbit escaped, SOI change,
/// and the 2 s per-(vehicle, kind) debounce.
/// </summary>
public sealed class EventDetectorTests
{
    // ----- baseline -------------------------------------------------------------------

    [Fact]
    public void FirstSample_IsABaselineAndEmitsNothing()
    {
        var detector = new EventDetector();

        IReadOnlyList<DetectedEvent> events = detector.Observe(TestData.Snapshot(situation: "landed"));

        Assert.Empty(events);
    }

    [Fact]
    public void SimTimeGoingBackwards_Rebaselines()
    {
        var detector = new EventDetector();
        detector.Observe(TestData.Snapshot(simT: 1000, situation: "landed"));

        // A save load rewinds Universe sim time; diffing across it would report a phantom
        // transition and leave every debounce timer in the future.
        IReadOnlyList<DetectedEvent> events = detector.Observe(TestData.Snapshot(simT: 5, situation: "freefall"));

        Assert.Empty(events);
    }

    // ----- situation ------------------------------------------------------------------

    [Fact]
    public void SituationChange_EmitsFromAndTo()
    {
        var detector = new EventDetector();
        detector.Observe(TestData.Snapshot(simT: 0, situation: "landed", altitudeM: 0));

        DetectedEvent detected = Assert.Single(detector.Observe(
            TestData.Snapshot(simT: 1, situation: "freefall", altitudeM: 120, surfaceSpeedMs: 45)));

        Assert.Equal(EventTypes.VehicleSituation, detected.Type);
        var payload = Assert.IsType<VehicleSituationPayload>(detected.Payload);
        Assert.Equal("landed", payload.From);
        Assert.Equal("freefall", payload.To);
        Assert.Equal("earth", payload.Body);
        Assert.Equal(120, payload.AltitudeM);
        Assert.Equal(45, payload.SurfaceSpeedMs);
    }

    [Fact]
    public void SituationChange_IsDebouncedForTwoSimSeconds()
    {
        var detector = new EventDetector();
        detector.Observe(TestData.Snapshot(simT: 0, situation: "landed"));

        Assert.Single(detector.Observe(TestData.Snapshot(simT: 1, situation: "rolling")));
        Assert.Empty(detector.Observe(TestData.Snapshot(simT: 1.5, situation: "landed")));
        Assert.Empty(detector.Observe(TestData.Snapshot(simT: 2.5, situation: "rolling")));
    }

    /// <summary>
    /// Debounce rate-limits; it must not lose the destination state. The next event that does fire
    /// reports <c>from</c> as the last state that actually reached the wire.
    /// </summary>
    [Fact]
    public void Debounce_ReportsFromTheLastReportedState()
    {
        var detector = new EventDetector();
        detector.Observe(TestData.Snapshot(simT: 0, situation: "landed"));

        Assert.Single(detector.Observe(TestData.Snapshot(simT: 1, situation: "rolling")));
        Assert.Empty(detector.Observe(TestData.Snapshot(simT: 2, situation: "freefall")));

        DetectedEvent detected = Assert.Single(detector.Observe(
            TestData.Snapshot(simT: 4, situation: "freefall")));
        var payload = Assert.IsType<VehicleSituationPayload>(detected.Payload);
        Assert.Equal("rolling", payload.From);
        Assert.Equal("freefall", payload.To);
    }

    [Fact]
    public void UnchangedSituation_EmitsNothing()
    {
        var detector = new EventDetector();
        detector.Observe(TestData.Snapshot(simT: 0, situation: "freefall"));

        Assert.Empty(detector.Observe(TestData.Snapshot(simT: 10, situation: "freefall")));
    }

    /// <summary>
    /// Optional snapshot fields ride through to the payload untouched when they are present.
    /// </summary>
    [Fact]
    public void SituationChange_CarriesTheRadarAltitudeWhenThereIsOne()
    {
        var detector = new EventDetector();
        detector.Observe(TestData.Snapshot(simT: 0, situation: "landed"));

        DetectedEvent detected = Assert.Single(
            detector.Observe(TestData.Snapshot(simT: 1, situation: "freefall", altitudeM: 4_000, radarAltM: 1_250)),
            static e => e.Kind == DetectKind.Situation);

        Assert.Equal(1_250, Assert.IsType<VehicleSituationPayload>(detected.Payload).RadarAltM);
    }

    /// <summary>
    /// And stay <c>null</c> when they are not — the barometric altitude beside them is a real
    /// number, so a zeroed radar altitude would read as a craft on the ground at 4 km.
    /// </summary>
    [Fact]
    public void SituationChange_LeavesTheRadarAltitudeNullWhenThereIsNone()
    {
        var detector = new EventDetector();
        detector.Observe(TestData.Snapshot(simT: 0, situation: "landed"));

        DetectedEvent detected = Assert.Single(
            detector.Observe(TestData.Snapshot(simT: 1, situation: "freefall", altitudeM: 4_000)),
            static e => e.Kind == DetectKind.Situation);

        Assert.Null(Assert.IsType<VehicleSituationPayload>(detected.Payload).RadarAltM);
    }

    // ----- landing: the same edge, seen from the other side ----------------------------

    /// <summary>
    /// The contact-free → contact transition emits <b>both</b> events off one detection, and the
    /// landing carries a <see cref="LandingObservation"/> rather than a finished payload because
    /// <c>survived</c> belongs to the correlator.
    /// </summary>
    [Fact]
    public void Touchdown_EmitsBothASituationChangeAndALanding()
    {
        var detector = new EventDetector();
        detector.Observe(TestData.Snapshot(simT: 0, situation: "freefall"));

        IReadOnlyList<DetectedEvent> events = detector.Observe(TestData.Snapshot(
            simT: 5, situation: "landed", verticalSpeedMs: 4.5, horizontalSpeedMs: 1.25,
            crewCount: 3, radarAltM: 0.4, lat: -28.5, lon: 152.75));

        Assert.Equal(
            [EventTypes.VehicleSituation, EventTypes.VehicleLanded],
            events.Select(static e => e.Type).ToArray());

        var landing = Assert.IsType<LandingObservation>(
            events.Single(static e => e.Kind == DetectKind.Landing).Payload);
        Assert.Equal("earth", landing.Body);
        Assert.Equal(4.5, landing.VerticalSpeedMs);
        Assert.Equal(1.25, landing.HorizontalSpeedMs);
        Assert.Equal(3, landing.CrewCount);
        Assert.Equal(0.4, landing.RadarAltM);
        Assert.Equal(-28.5, landing.Lat);
        Assert.Equal(152.75, landing.Lon);
    }

    /// <summary>A landing where the position could not be read omits it rather than claiming (0, 0).</summary>
    [Fact]
    public void Touchdown_WithNoReadablePosition_LeavesLatLonNull()
    {
        var detector = new EventDetector();
        detector.Observe(TestData.Snapshot(simT: 0, situation: "freefall"));

        DetectedEvent detected = Assert.Single(
            detector.Observe(TestData.Snapshot(simT: 5, situation: "landed")),
            static e => e.Kind == DetectKind.Landing);

        var landing = Assert.IsType<LandingObservation>(detected.Payload);
        Assert.Null(landing.Lat);
        Assert.Null(landing.Lon);
        Assert.Null(landing.RadarAltM);
    }

    /// <summary>Every surface-contact situation counts, not only <c>landed</c>.</summary>
    [Theory]
    [InlineData("landed")]
    [InlineData("rolling")]
    [InlineData("sailing")]
    [InlineData("floating")]
    [InlineData("dragging")]
    [InlineData("bottomed")]
    public void Touchdown_FiresForEverySurfaceContactSituation(string situation)
    {
        var detector = new EventDetector();
        detector.Observe(TestData.Snapshot(simT: 0, situation: "maneuvering"));

        Assert.Single(
            detector.Observe(TestData.Snapshot(simT: 5, situation: situation)),
            static e => e.Kind == DetectKind.Landing);
    }

    /// <summary>
    /// Contact → contact is a taxi, not a landing; contact → contact-free is a liftoff. Only the
    /// one edge produces the event.
    /// </summary>
    [Theory]
    [InlineData("landed", "rolling")]
    [InlineData("floating", "sailing")]
    [InlineData("landed", "freefall")]
    [InlineData("rolling", "maneuvering")]
    public void NonTouchdownTransitions_EmitOnlyTheSituationChange(string from, string to)
    {
        var detector = new EventDetector();
        detector.Observe(TestData.Snapshot(simT: 0, situation: from));

        DetectedEvent only = Assert.Single(detector.Observe(TestData.Snapshot(simT: 5, situation: to)));
        Assert.Equal(DetectKind.Situation, only.Kind);
    }

    /// <summary>
    /// A situation name catlog does not know reports "no contact" by construction, and that must
    /// not be mistaken for flight: leaving an unknown state for the ground is not a landing anyone
    /// can vouch for.
    /// </summary>
    [Fact]
    public void Touchdown_FromAnUnknownSituation_IsNotClaimed()
    {
        var detector = new EventDetector();
        detector.Observe(TestData.Snapshot(simT: 0, situation: "unknown"));

        DetectedEvent only = Assert.Single(detector.Observe(TestData.Snapshot(simT: 5, situation: "landed")));
        Assert.Equal(DetectKind.Situation, only.Kind);
    }

    /// <summary>
    /// The landing shares the situation rule's debounce rather than owning one — a lander bouncing
    /// between <c>freefall</c> and <c>landed</c> at 2 Hz would otherwise mint a record every
    /// 500 ms — and it is not lost by it either: the latch only advances when the pair actually
    /// fires, so the next sample past the window emits both.
    /// </summary>
    [Fact]
    public void Touchdown_InheritsTheSituationDebounceWithoutLosingTheEdge()
    {
        var detector = new EventDetector();
        detector.Observe(TestData.Snapshot(simT: 0, situation: "freefall"));

        Assert.Single(
            detector.Observe(TestData.Snapshot(simT: 1, situation: "landed")),
            static e => e.Kind == DetectKind.Landing);

        // Bounce back off the surface and down again, all inside the 2 s window: nothing at all.
        Assert.Empty(detector.Observe(TestData.Snapshot(simT: 1.5, situation: "freefall")));
        Assert.Empty(detector.Observe(TestData.Snapshot(simT: 2.5, situation: "landed")));

        // Past the window the pending edge is still there — reported from the last state that
        // reached the wire, which is `landed`, so it is a taxi and not a second landing.
        DetectedEvent settled = Assert.Single(detector.Observe(TestData.Snapshot(simT: 4, situation: "rolling")));
        Assert.Equal(DetectKind.Situation, settled.Kind);
        Assert.Equal("landed", Assert.IsType<VehicleSituationPayload>(settled.Payload).From);
    }

    /// <summary>
    /// A vehicle that is already on the ground when catlog first sees it did not land: the first
    /// sample is a baseline, and replaying the state of the world at save-load as events is the
    /// storm the baseline exists to prevent.
    /// </summary>
    [Fact]
    public void Touchdown_IsNotClaimedForAVehicleFirstSeenOnTheGround()
    {
        var detector = new EventDetector();

        Assert.Empty(detector.Observe(TestData.Snapshot(simT: 0, situation: "landed")));
        Assert.Empty(detector.Observe(TestData.Snapshot(simT: 5, situation: "landed")));
    }

    // ----- atmosphere hysteresis ------------------------------------------------------

    [Fact]
    public void AtmosphereEntry_NeedsToCrossTheLowerHysteresisEdge()
    {
        var detector = new EventDetector();
        // Baseline well above the atmosphere.
        detector.Observe(TestData.Snapshot(simT: 0, altitudeM: 200_000));

        // 69 500 m is below the 70 000 m nominal ceiling but above 68 600 m (= 70 000 × 0.98).
        Assert.Empty(detector.Observe(TestData.Snapshot(simT: 5, altitudeM: 69_500)));

        DetectedEvent detected = Assert.Single(detector.Observe(
            TestData.Snapshot(simT: 10, altitudeM: 68_000, surfaceSpeedMs: 2_400, dynPressurePa: 15)));
        var payload = Assert.IsType<VehicleAtmospherePayload>(detected.Payload);
        Assert.Equal("entered", payload.Dir);
        Assert.Equal(2_400, payload.SpeedMs);
        Assert.Equal(15, payload.DynPressurePa);
    }

    [Fact]
    public void AtmosphereExit_NeedsToCrossTheUpperHysteresisEdge()
    {
        var detector = new EventDetector();
        detector.Observe(TestData.Snapshot(simT: 0, altitudeM: 10_000)); // baseline inside

        // 70 500 m is above the nominal ceiling but below 71 400 m (= 70 000 × 1.02).
        Assert.Empty(detector.Observe(TestData.Snapshot(simT: 5, altitudeM: 70_500)));

        DetectedEvent detected = Assert.Single(detector.Observe(
            TestData.Snapshot(simT: 10, altitudeM: 72_000)));
        var payload = Assert.IsType<VehicleAtmospherePayload>(detected.Payload);
        Assert.Equal("exited", payload.Dir);
    }

    /// <summary>
    /// The reason hysteresis exists at all: a vehicle hovering on the nominal boundary must not
    /// alternate. Debounce alone only rate-limits the storm, it does not suppress it.
    /// </summary>
    [Fact]
    public void HoveringOnTheBoundary_DoesNotFlap()
    {
        var detector = new EventDetector();
        detector.Observe(TestData.Snapshot(simT: 0, altitudeM: 200_000));

        int fired = 0;
        for (int i = 1; i <= 40; i++)
        {
            double altitude = i % 2 == 0 ? 69_900 : 70_100; // straddling 70 000 m
            fired += detector.Observe(TestData.Snapshot(simT: i * 5.0, altitudeM: altitude)).Count;
        }

        Assert.Equal(0, fired);
    }

    [Fact]
    public void AirlessBody_NeverEntersTheAtmosphere()
    {
        var detector = new EventDetector();
        detector.Observe(TestData.Snapshot(simT: 0, body: "moon", atmoHeightM: 0, altitudeM: 50_000));

        Assert.Empty(detector.Observe(TestData.Snapshot(simT: 5, body: "moon", atmoHeightM: 0, altitudeM: 10)));
    }

    // ----- orbit ----------------------------------------------------------------------

    [Fact]
    public void OrbitAchieved_WhenPeriapsisClearsAtmospherePlusMargin()
    {
        var detector = new EventDetector();
        detector.Observe(TestData.Snapshot(simT: 0, peAltM: 60_000, apAltM: 200_000, ecc: 0.4,
            orbitClass: OrbitClass.Bound));

        // Threshold is 70 000 + 1 000 = 71 000 m.
        Assert.Empty(detector.Observe(TestData.Snapshot(simT: 5, peAltM: 70_500, apAltM: 200_000,
            ecc: 0.3, orbitClass: OrbitClass.Bound)));

        DetectedEvent detected = Assert.Single(detector.Observe(TestData.Snapshot(
            simT: 10, peAltM: 180_000, apAltM: 220_000, ecc: 0.01, incDeg: 51.6,
            smaM: 6_571_000, lanDeg: 72.25, argpDeg: 14.75, tPe: 8.5, periodS: 5_420.5,
            orbitClass: OrbitClass.Bound)));
        var payload = Assert.IsType<VehicleOrbitPayload>(detected.Payload);
        Assert.Equal("achieved", payload.Phase);
        Assert.Equal(220_000, payload.ApM);
        Assert.Equal(180_000, payload.PeM);
        Assert.Equal(51.6, payload.IncDeg);
        Assert.Equal(6_571_000, payload.SmaM);
        Assert.Equal(72.25, payload.LanDeg);
        Assert.Equal(14.75, payload.ArgpDeg);
        Assert.Equal(8.5, payload.TPe);
        Assert.Equal(5_420.5, payload.PeriodS);

        // The mass at the milestone, which is what makes it comparable with flight.started.mass_kg.
        Assert.Equal(1_000, payload.MassKg);
    }

    [Fact]
    public void OrbitAchieved_FiresOnceUntilItFallsBackBelowTheBar()
    {
        var detector = new EventDetector();
        detector.Observe(TestData.Snapshot(simT: 0, peAltM: 10_000, orbitClass: OrbitClass.Bound));

        Assert.Single(detector.Observe(TestData.Snapshot(simT: 10, peAltM: 200_000, orbitClass: OrbitClass.Bound)));
        Assert.Empty(detector.Observe(TestData.Snapshot(simT: 20, peAltM: 210_000, orbitClass: OrbitClass.Bound)));
        Assert.Empty(detector.Observe(TestData.Snapshot(simT: 30, peAltM: 10_000, orbitClass: OrbitClass.Bound)));
        Assert.Single(detector.Observe(TestData.Snapshot(simT: 40, peAltM: 200_000, orbitClass: OrbitClass.Bound)));
    }

    [Fact]
    public void OrbitPayloadCarriesTheCaptureBoundaryZeroFallbacks()
    {
        var detector = new EventDetector();
        detector.Observe(TestData.Snapshot(simT: 0, peAltM: 0, orbitClass: OrbitClass.Bound));

        DetectedEvent detected = Assert.Single(detector.Observe(TestData.Snapshot(
            simT: 10, peAltM: 200_000, orbitClass: OrbitClass.Bound)));
        var payload = Assert.IsType<VehicleOrbitPayload>(detected.Payload);

        Assert.Equal(0, payload.SmaM);
        Assert.Equal(0, payload.LanDeg);
        Assert.Equal(0, payload.ArgpDeg);
        Assert.Equal(0, payload.TPe);
        Assert.Equal(0, payload.PeriodS);
    }

    [Fact]
    public void AirlessBody_OrbitAchievedUsesTheBareThousandMetreMargin()
    {
        var detector = new EventDetector();
        detector.Observe(TestData.Snapshot(simT: 0, body: "moon", atmoHeightM: 0, peAltM: 500,
            orbitClass: OrbitClass.Bound));

        Assert.Single(detector.Observe(TestData.Snapshot(simT: 10, body: "moon", atmoHeightM: 0,
            peAltM: 1_500, orbitClass: OrbitClass.Bound)));
    }

    /// <summary>
    /// The case the plan calls out. A hyperbolic orbit has a <b>negative</b> apoapsis, not NaN
    /// (docs/ksa-integration.md B4) — so nothing here may sniff for NaN, and the escape must fire
    /// off the conic class the game supplied.
    /// </summary>
    [Fact]
    public void OrbitEscaped_OnAHyperbolicOrbitWithNegativeApoapsis()
    {
        var detector = new EventDetector();
        detector.Observe(TestData.Snapshot(simT: 0, ecc: 0.7, apAltM: 400_000, peAltM: 200_000,
            orbitClass: OrbitClass.Bound));

        DetectedEvent detected = Assert.Single(detector.Observe(TestData.Snapshot(
            simT: 10, ecc: 1.4, apAltM: -5_000_000, peAltM: 250_000, orbitClass: OrbitClass.Hyperbolic)));
        var payload = Assert.IsType<VehicleOrbitPayload>(detected.Payload);
        Assert.Equal("escaped", payload.Phase);
        Assert.Equal(1.4, payload.Ecc);
        Assert.Equal(-5_000_000, payload.ApM);
        Assert.Equal(0, payload.PeriodS);
    }

    [Fact]
    public void OrbitEscaped_ZeroesAStaleFinitePeriodOnAnUnboundSnapshot()
    {
        var detector = new EventDetector();
        detector.Observe(TestData.Snapshot(simT: 0, ecc: 0.7, periodS: 4_200,
            orbitClass: OrbitClass.Bound));

        DetectedEvent detected = Assert.Single(detector.Observe(TestData.Snapshot(
            simT: 10, ecc: 1.4, periodS: 4_200, orbitClass: OrbitClass.Hyperbolic)));

        Assert.Equal(0, Assert.IsType<VehicleOrbitPayload>(detected.Payload).PeriodS);
    }

    [Fact]
    public void OrbitEscaped_OnAParabolicOrbitWithNanApoapsis()
    {
        var detector = new EventDetector();
        detector.Observe(TestData.Snapshot(simT: 0, ecc: 0.9, orbitClass: OrbitClass.Bound));

        DetectedEvent detected = Assert.Single(detector.Observe(TestData.Snapshot(
            simT: 10, ecc: 1.0, apAltM: double.NaN, periodS: 99,
            orbitClass: OrbitClass.Parabolic)));
        var payload = Assert.IsType<VehicleOrbitPayload>(detected.Payload);
        Assert.Equal("escaped", payload.Phase);
        Assert.Equal(0, payload.PeriodS);
    }

    /// <summary>
    /// When the game project did not supply a conic class (the simulator, hand-built fixtures),
    /// the fallback is a finite <c>ecc &lt; 1</c> — still never a NaN sniff on apoapsis.
    /// </summary>
    [Fact]
    public void OrbitEscaped_FallsBackToEccentricityWhenTheClassIsUnknown()
    {
        var detector = new EventDetector();
        detector.Observe(TestData.Snapshot(simT: 0, ecc: 0.2));

        Assert.Single(detector.Observe(TestData.Snapshot(simT: 10, ecc: 1.6)));
    }

    [Fact]
    public void NonFiniteEccentricityWithNoClass_IsTreatedAsUnbound()
    {
        // Capture sanitizes NaN to 0, so this should not arise; if it ever does, an unclassifiable
        // orbit must not be reported as a stable one.
        var snapshot = TestData.Snapshot(ecc: double.NaN);

        Assert.False(snapshot.IsBoundOrbit);
    }

    [Fact]
    public void OrbitEscaped_ThenRecaptured_CanFireAgain()
    {
        var detector = new EventDetector();
        detector.Observe(TestData.Snapshot(simT: 0, ecc: 0.2, orbitClass: OrbitClass.Bound));

        Assert.Single(detector.Observe(TestData.Snapshot(simT: 10, ecc: 1.4, orbitClass: OrbitClass.Hyperbolic)));
        Assert.Empty(detector.Observe(TestData.Snapshot(simT: 20, ecc: 0.5, orbitClass: OrbitClass.Bound)));
        Assert.Single(detector.Observe(TestData.Snapshot(simT: 30, ecc: 2.0, orbitClass: OrbitClass.Hyperbolic)));
    }

    // ----- SOI ------------------------------------------------------------------------

    [Fact]
    public void SoiChange_EmitsFromAndToBodies()
    {
        var detector = new EventDetector();
        detector.Observe(TestData.Snapshot(simT: 0, body: "earth"));

        DetectedEvent detected = Assert.Single(
            detector.Observe(TestData.Snapshot(simT: 10, body: "moon")),
            static e => e.Kind == DetectKind.SoiChange);
        var payload = Assert.IsType<VehicleSoiPayload>(detected.Payload);
        Assert.Equal("earth", payload.FromBody);
        Assert.Equal("moon", payload.ToBody);
    }

    /// <summary>
    /// A failed parent read must never look like leaving every sphere of influence. unscience's
    /// zeroed fallback snapshot manufactures exactly this phantom (<c>ParentBodyId = ""</c>), which
    /// is why catlog's sampler omits the vehicle instead — but the detector guards too.
    /// </summary>
    [Fact]
    public void SoiChange_IgnoresABlankParentBody()
    {
        var detector = new EventDetector();
        detector.Observe(TestData.Snapshot(simT: 0, body: "earth"));

        Assert.Empty(detector.Observe(TestData.Snapshot(simT: 10, body: "", parentBodyId: "")));
    }

    [Fact]
    public void SoiChange_IsDebounced()
    {
        var detector = new EventDetector();
        detector.Observe(TestData.Snapshot(simT: 0, body: "earth"));

        Assert.NotEmpty(detector.Observe(TestData.Snapshot(simT: 1, body: "moon")));
        Assert.DoesNotContain(
            detector.Observe(TestData.Snapshot(simT: 2, body: "earth")),
            static e => e.Kind == DetectKind.SoiChange);
    }

    // ----- state lifecycle ------------------------------------------------------------

    [Fact]
    public void MultipleVehicles_HaveIndependentState()
    {
        var detector = new EventDetector();
        detector.Observe(TestData.Frame(1,
            TestData.Snapshot(vehicleId: "a", simT: 0, situation: "landed"),
            TestData.Snapshot(vehicleId: "b", simT: 0, situation: "freefall")));

        IReadOnlyList<DetectedEvent> events = detector.Observe(TestData.Frame(2,
            TestData.Snapshot(vehicleId: "a", simT: 5, situation: "rolling"),
            TestData.Snapshot(vehicleId: "b", simT: 5, situation: "freefall")));

        DetectedEvent only = Assert.Single(events);
        Assert.Equal("a", only.VehicleId);
        Assert.Equal(2, detector.TrackedVehicles);
    }

    [Fact]
    public void Prune_DropsStateForVehiclesNoLongerPresent()
    {
        var detector = new EventDetector();
        detector.Observe(TestData.Frame(1,
            TestData.Snapshot(vehicleId: "a"),
            TestData.Snapshot(vehicleId: "b")));

        detector.Prune(new HashSet<string> { "a" });

        Assert.Equal(1, detector.TrackedVehicles);
    }

    [Fact]
    public void Forget_DropsOneVehicle()
    {
        var detector = new EventDetector();
        detector.Observe(TestData.Snapshot(vehicleId: "a"));
        detector.Forget("a");

        Assert.Equal(0, detector.TrackedVehicles);
        Assert.Empty(detector.Observe(TestData.Snapshot(vehicleId: "a", situation: "landed")));
    }
}
