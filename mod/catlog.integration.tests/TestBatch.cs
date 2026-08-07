using System;
using System.Collections.Generic;
using MeowSci.Catlog.Lib.Detect;
using MeowSci.Catlog.Lib.Events;
using MeowSci.Catlog.Lib.Util;

namespace MeowSci.Catlog.Integration.Tests;

/// <summary>
/// Builds batches the way the mod builds them: signals in, <see cref="EventPipeline"/> in the
/// middle, envelopes out.
/// </summary>
/// <remarks>
/// Hand-writing envelopes here would test the server's parser against a fixture instead of against
/// the mod. Driving the real pipeline means a change to <c>ver</c>, to a payload's JSON names, or
/// to the envelope shape shows up as a <c>400 malformed_batch</c> in this suite rather than in
/// production.
/// </remarks>
internal static class TestBatch
{
    /// <summary>Builds exactly <paramref name="count"/> envelopes for one short flight.</summary>
    /// <param name="count">How many envelopes to produce; at least 2.</param>
    /// <returns>The envelopes, in emission order.</returns>
    internal static IReadOnlyList<EventEnvelope> Build(int count)
    {
        if (count < 2)
            throw new ArgumentOutOfRangeException(nameof(count), count, "A batch needs a session and a flight.");

        var pipeline = new EventPipeline(new EventPipelineOptions(
            InstallId: Ids.NewUlid(),
            ModVersion: "0.1.0-itest",
            GameBuild: "2026.8.5.5168"));

        long wall = DateTimeOffset.UtcNow.ToUnixTimeMilliseconds();
        var events = new List<EventEnvelope>(count) { pipeline.SessionStarted(0, wall) };

        events.AddRange(pipeline.ProcessSignal(new VehicleCreatedSignal(
            0, wall, "itest-1", "Integration Test Article", "kerbin", 12_400, 31, 2, LaunchGameTime: 0)));

        // Stagings are the cheapest one-signal-one-event pairing in §4.2, which is what makes the
        // count exact — and an exact count is what the 413 halving ladder needs.
        for (int i = 0; events.Count < count; i++)
        {
            double simT = 1.0 + i;
            events.AddRange(pipeline.ProcessSignal(
                new StagingSignal(simT, wall + (long)(simT * 1000), "itest-1", i)));
        }

        return events;
    }

    /// <summary>Builds a batch and renders it as the NDJSON body §4.3 specifies.</summary>
    /// <param name="count">How many envelopes.</param>
    /// <returns>The UTF-8 body.</returns>
    internal static byte[] Ndjson(int count) => IngestClient.Ndjson(Build(count));
}
