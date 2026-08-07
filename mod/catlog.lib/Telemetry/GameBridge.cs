using System;
using System.Collections.Generic;
using System.Threading;
using System.Threading.Channels;
using MeowSci.Catlog.Lib.Events;
using MeowSci.Catlog.Lib.Util;

namespace MeowSci.Catlog.Lib.Telemetry;

/// <summary>
/// The game-thread → worker seam, and the only place the game project (WP8) touches
/// <c>catlog.lib</c> from inside a frame. Every method on the producer side is non-blocking and
/// allocation-light; nothing here ever throws at the caller.
/// </summary>
/// <remarks>
/// <para>
/// <b>Two paths, on purpose.</b> Passive telemetry goes through <see cref="Frames"/>, a
/// latest-wins <see cref="SnapshotStore"/>: a worker that falls behind drops intermediate frames,
/// which costs sample resolution and nothing else because the detector compares prev/curr.
/// Discrete <see cref="GameSignal"/>s go through <see cref="Signals"/>, an <b>unbounded
/// lossless</b> channel: a RUD, an impact, a staging or a tumble is a scoring event, and dropping
/// one is a permanently lost leaderboard entry. Putting signals in a latest-wins slot — the
/// obvious "just publish everything in the frame" design — silently eats them under load. Do not
/// merge the two paths.
/// </para>
/// <para>
/// The channel is unbounded rather than bounded-with-drop for the same reason. Bounded growth is
/// still capped downstream: the worker appends to the SQLite outbox, which has its own size cap
/// and prune policy, and prune drops passive rows before it drops anything scoring.
/// </para>
/// <para>
/// <b>Frame grouping survives the hand-off</b> because <see cref="EndFrame"/> writes a
/// <see cref="FrameBoundarySignal"/> in-band. <see cref="Detect.ImpactCorrelator"/> needs to know
/// which impacts and destructions happened in the same frame, and channel order is the only thing
/// that still carries that once the signals leave the game thread.
/// </para>
/// <para>
/// Typical WP8 wiring, all on the game thread:
/// </para>
/// <code>
/// // Harmony patch bodies:
/// bridge.Signal(new ImpactSignal(simT, wallMs, id, v, e, isPad, body, crew));
///
/// // once per frame, after Universe.ApplyVehicleSolvers + InputEvents.ApplyInputEvents:
/// if (sampleClock.Tick(dt))
///     bridge.PublishFrame(simT, wallMs, snapshots);
/// bridge.EndFrame(simT, wallMs);
/// </code>
/// </remarks>
public sealed class GameBridge
{
    private readonly Channel<GameSignal> _signals = Channel.CreateUnbounded<GameSignal>(
        new UnboundedChannelOptions
        {
            // One game thread writes; one worker reads.
            SingleWriter = true,
            SingleReader = true,
            AllowSynchronousContinuations = false,
        });

    private long _sequence;
    private long _signalsWritten;
    private long _signalsDropped;
    private bool _writeErrorLogged;

    /// <summary>The latest-wins passive telemetry exchange.</summary>
    public SnapshotStore Frames { get; } = new();

    /// <summary>The lossless discrete-signal stream, in game-thread order.</summary>
    public ChannelReader<GameSignal> Signals => _signals.Reader;

    /// <summary>How many signals have been accepted since construction.</summary>
    public long SignalsWritten => Interlocked.Read(ref _signalsWritten);

    /// <summary>
    /// How many signals were refused. Non-zero means the channel was completed while the game
    /// thread was still running — surface it, never ignore it.
    /// </summary>
    public long SignalsDropped => Interlocked.Read(ref _signalsDropped);

    /// <summary>
    /// Publishes one sample pass. Game thread only, single writer. Stamps the frame with the next
    /// sequence number.
    /// </summary>
    /// <param name="simT">Universe sim seconds at capture.</param>
    /// <param name="wallMs">Client unix milliseconds at capture.</param>
    /// <param name="vehicles">
    /// One snapshot per vehicle that sampled successfully. A vehicle whose read threw must be
    /// <b>absent</b> from this list — never zero-filled. A zeroed fallback snapshot fed to a
    /// prev/curr comparator manufactures phantom SOI changes (parent body → <c>""</c>) and phantom
    /// orbit-achieved edges (eccentricity → 0), both of which score.
    /// </param>
    /// <returns>The frame that was published.</returns>
    public TelemetryFrame PublishFrame(double simT, long wallMs, IReadOnlyList<TelemetrySnapshot> vehicles)
    {
        var frame = new TelemetryFrame(++_sequence, simT, wallMs, vehicles);
        Frames.Publish(frame);
        return frame;
    }

    /// <summary>
    /// Enqueues a discrete signal. Game thread only. Never throws and never blocks.
    /// </summary>
    /// <param name="signal">The signal.</param>
    /// <returns>True when the signal was accepted.</returns>
    public bool Signal(GameSignal signal)
    {
        if (signal is null)
            return false;

        if (_signals.Writer.TryWrite(signal))
        {
            Interlocked.Increment(ref _signalsWritten);
            return true;
        }

        Interlocked.Increment(ref _signalsDropped);
        if (!_writeErrorLogged)
        {
            _writeErrorLogged = true;
            ModLog.Log.Error(
                "catlog: a game signal was refused by the worker channel (logged once). "
                + "Scoring events are being lost for the rest of this session.");
        }

        return false;
    }

    /// <summary>
    /// Closes the current frame in the signal stream. Call once per game frame, after the game's
    /// own solver-apply and input-apply passes have run — that is the point at which every impact
    /// and every destruction for the frame has landed.
    /// </summary>
    /// <param name="simT">Universe sim seconds at the boundary.</param>
    /// <param name="wallMs">Client unix milliseconds at the boundary.</param>
    /// <returns>True when the boundary marker was accepted.</returns>
    public bool EndFrame(double simT, long wallMs)
        => Signal(new FrameBoundarySignal(simT, wallMs, Interlocked.Read(ref _sequence)));

    /// <summary>
    /// Signals that no further signals will be written, so the worker's read loop can drain and
    /// finish. Idempotent.
    /// </summary>
    public void Complete() => _signals.Writer.TryComplete();

    /// <summary>
    /// Drains every signal currently buffered, without waiting. Used by the simulator and tests,
    /// which pump the pipeline synchronously.
    /// </summary>
    /// <returns>The buffered signals in write order.</returns>
    public IReadOnlyList<GameSignal> DrainSignals()
    {
        List<GameSignal>? drained = null;
        while (_signals.Reader.TryRead(out GameSignal? signal))
            (drained ??= []).Add(signal);
        return drained ?? (IReadOnlyList<GameSignal>)Array.Empty<GameSignal>();
    }
}
