using System.Text.Json;
using MeowSci.Catlog.Lib.Events;
using MeowSci.Catlog.Lib.Util;
using Xunit;

namespace MeowSci.Catlog.Lib.Tests.Events;

/// <summary>§4.1: the normative envelope shared with the Go server.</summary>
public sealed class EventEnvelopeTests
{
    [Fact]
    public void SerializesTheExactContractKeys()
    {
        EventEnvelope envelope = TestData.Envelope(
            type: EventTypes.VehicleRud,
            id: "01J9V5M3E8Z0FAKEULID26CHR",
            simT: 12345.678,
            wallMs: 1_770_000_000_123,
            payload: new VehicleRudPayload("ground_impact", 41.5, 0, 220, 0, "earth", 2, null, null));

        using JsonDocument document = JsonDocument.Parse(envelope.ToNdjsonLine());
        JsonElement root = document.RootElement;

        Assert.Equal("01J9V5M3E8Z0FAKEULID26CHR", root.GetProperty("id").GetString());
        Assert.Equal("vehicle.rud", root.GetProperty("type").GetString());
        Assert.Equal(2, root.GetProperty("ver").GetInt32());
        Assert.Equal("01J9V5M3E8Z0FAKEFLIGHT001", root.GetProperty("flight").GetString());
        Assert.Equal("01J9V5M3E8Z0FAKESESSION01", root.GetProperty("session").GetString());
        Assert.Equal(12345.678, root.GetProperty("sim_t").GetDouble());
        Assert.Equal(1_770_000_000_123, root.GetProperty("wall_t").GetInt64());
        Assert.Equal("ground_impact", root.GetProperty("payload").GetProperty("cause").GetString());
        Assert.Equal(41.5, root.GetProperty("payload").GetProperty("peak_g").GetDouble());
    }

    /// <summary>
    /// §4.1 spells the field as <c>"flight": null</c> for session and roster events — the key is
    /// present, not omitted, so the Go decoder's strict unknown/missing handling stays simple.
    /// </summary>
    [Fact]
    public void NullFlight_IsSerializedAsAnExplicitNull()
    {
        EventEnvelope envelope = TestData.Envelope(
            type: EventTypes.SessionStarted,
            flight: null,
            payload: new SessionStartedPayload("0.1.0", "2026.8.5.5168", TestData.InstallId));

        using JsonDocument document = JsonDocument.Parse(envelope.ToNdjsonLine());

        Assert.True(document.RootElement.TryGetProperty("flight", out JsonElement flight));
        Assert.Equal(JsonValueKind.Null, flight.ValueKind);
    }

    [Fact]
    public void ToNdjsonLine_NeverContainsANewline()
    {
        EventEnvelope envelope = TestData.Envelope(
            payload: new FlightFlaggedPayload("console", "line one\nline two"));

        string line = envelope.ToNdjsonLine();

        Assert.DoesNotContain('\n', line);
        Assert.Contains("line one\\nline two", line);
    }

    [Fact]
    public void Create_MintsAUlidAndPicksUpTheRegistryVersion()
    {
        EventEnvelope envelope = EventEnvelope.Create(
            EventTypes.VehicleStaging, "session", TestData.CareerId, "flight", 1, 2,
            new VehicleStagingPayload(3));

        Assert.True(Ids.IsUlid(envelope.Id));
        Assert.Equal(EventTypes.VersionOf(EventTypes.VehicleStaging), envelope.Ver);
    }

    /// <summary>
    /// §4.2 omits <c>peak_g</c>/<c>max_q_pa</c> when the game had no reading — reporting 0 would
    /// corrupt the peak-g board (DECISIONS 2026-08-06).
    /// </summary>
    [Fact]
    public void TelemetryWindow_OmitsPeakGAndMaxQWhenNull()
    {
        var payload = new TelemetryWindowPayload(
            T0Sim: 0, T1Sim: 29.5, N: 60, Body: "earth",
            AltM: new Agg(0, 1, 0.5, 1), SurfaceSpeedMs: new Agg(0, 1, 0.5, 1),
            OrbitalSpeedMs: new Agg(0, 1, 0.5, 1), AccelMs2: new Agg(0, 1, 0.5, 1),
            PeakG: null, MaxQPa: null, MassKgLast: 1_000, RadarAltM: null, WarpMax: 1);

        using JsonDocument document = JsonDocument.Parse(
            TestData.Envelope(type: EventTypes.TelemetryWindow, payload: payload).ToNdjsonLine());
        JsonElement body = document.RootElement.GetProperty("payload");

        Assert.False(body.TryGetProperty("peak_g", out _), "peak_g must be absent, not zero");
        Assert.False(body.TryGetProperty("max_q_pa", out _), "max_q_pa must be absent, not zero");
        Assert.Equal(1_000, body.GetProperty("mass_kg_last").GetDouble());
    }

    [Fact]
    public void TelemetryWindow_EmitsPeakGWhenPresent()
    {
        var payload = new TelemetryWindowPayload(
            T0Sim: 0, T1Sim: 29.5, N: 60, Body: "earth",
            AltM: new Agg(0, 1, 0.5, 1), SurfaceSpeedMs: new Agg(0, 1, 0.5, 1),
            OrbitalSpeedMs: new Agg(0, 1, 0.5, 1), AccelMs2: new Agg(0, 1, 0.5, 1),
            PeakG: 4.25, MaxQPa: 31_000, MassKgLast: 1_000,
            RadarAltM: new Agg(2, 900, 400, 2), WarpMax: 1);

        using JsonDocument document = JsonDocument.Parse(
            TestData.Envelope(type: EventTypes.TelemetryWindow, payload: payload).ToNdjsonLine());
        JsonElement body = document.RootElement.GetProperty("payload");

        Assert.Equal(4.25, body.GetProperty("peak_g").GetDouble());
        Assert.Equal(31_000, body.GetProperty("max_q_pa").GetDouble());
    }

    [Fact]
    public void AggSerializesAsMinMaxMeanLast()
    {
        using JsonDocument document = JsonDocument.Parse(CatlogJson.Serialize(new Agg(1, 4, 2.5, 3)));
        JsonElement root = document.RootElement;

        Assert.Equal(1, root.GetProperty("min").GetDouble());
        Assert.Equal(4, root.GetProperty("max").GetDouble());
        Assert.Equal(2.5, root.GetProperty("mean").GetDouble());
        Assert.Equal(3, root.GetProperty("last").GetDouble());
    }
}
