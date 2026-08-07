using System;
using System.Collections.Generic;
using System.Globalization;
using System.Security.Cryptography;
using System.Text;

namespace MeowSci.Catlog.LoadGen;

/// <summary>
/// The harness's random source: SplitMix64, implemented here rather than taken from
/// <see cref="Random"/>.
/// </summary>
/// <remarks>
/// <para>
/// <b>Why not <see cref="Random"/>.</b> <c>--seed</c> is a promise that a failing run can be
/// replayed, and <see cref="Random"/>'s seeded stream is an implementation detail of the runtime
/// (it already changed once, in .NET 6). Thirty lines of SplitMix64 make the promise hold across
/// runtimes and machines.
/// </para>
/// <para>
/// <b>How reproducibility survives concurrency.</b> There is no shared generator. Player <c>i</c>
/// gets its own instance seeded from <see cref="ForPlayer"/>, a pure function of the run seed and
/// the player index, and draws from it in a fixed order on one thread. Nothing about the order in
/// which players are scheduled, how many run at once, or how long the server takes to answer can
/// reach a draw — so the <i>stream</i> a player draws from is a function of <c>(seed, index)</c>
/// alone.
/// </para>
/// <para>
/// <b>The one thing that is not.</b> Two coverage rotations — the career-ladder rung and the
/// covering <c>vehicle.rud</c> cause — are keyed on the player's dense position among the players
/// that actually ran, not on the account index, because identities refused by the ≥30-day age gate
/// leave holes in the indices and a rotation with holes drops whole rungs. That population is
/// itself a deterministic function of the run's flags (<c>--players</c> and <c>--too-new</c> decide
/// exactly which subjects mockidp mints too young), so a re-run of the same seed <i>with the same
/// flags</i> reproduces the same careers and the same digest — which is the promise <c>--seed</c>
/// actually makes. A run in which provisioning fails for some other reason has a different
/// population and is a different run; the digest says so, which is the point of printing it.
/// </para>
/// </remarks>
internal sealed class Prng
{
    private ulong _state;

    /// <summary>Creates a generator.</summary>
    /// <param name="seed">The seed; any value, including zero.</param>
    internal Prng(ulong seed) => _state = seed;

    /// <summary>
    /// The generator for one player: a pure function of the run seed and the player's index.
    /// </summary>
    /// <param name="runSeed">The <c>--seed</c> value.</param>
    /// <param name="playerIndex">The player's zero-based index.</param>
    /// <returns>That player's generator.</returns>
    internal static Prng ForPlayer(long runSeed, int playerIndex)
        => new(Mix((ulong)runSeed * 0x9E37_79B9_7F4A_7C15UL + (ulong)(playerIndex + 1) * 0xBF58_476D_1CE4_E5B9UL));

    /// <summary>The next 64 raw bits.</summary>
    /// <returns>The value.</returns>
    internal ulong Next()
    {
        _state += 0x9E37_79B9_7F4A_7C15UL;
        return Mix(_state);
    }

    /// <summary>A uniform draw in <c>[0, 1)</c>.</summary>
    /// <returns>The value.</returns>
    internal double NextDouble() => (Next() >> 11) * (1.0 / 9007199254740992.0);

    /// <summary>A uniform integer in <c>[min, max)</c>.</summary>
    /// <param name="minInclusive">Lower bound.</param>
    /// <param name="maxExclusive">Upper bound; returns <paramref name="minInclusive"/> when not above it.</param>
    /// <returns>The value.</returns>
    internal int Int(int minInclusive, int maxExclusive)
        => maxExclusive <= minInclusive
            ? minInclusive
            : minInclusive + (int)(Next() % (ulong)(maxExclusive - minInclusive));

    /// <summary>A uniform double in <c>[lo, hi]</c>.</summary>
    /// <param name="lo">Lower bound.</param>
    /// <param name="hi">Upper bound.</param>
    /// <returns>The value.</returns>
    internal double Range(double lo, double hi) => lo + ((hi - lo) * NextDouble());

    /// <summary>True with probability <paramref name="p"/>.</summary>
    /// <param name="p">Probability in <c>[0, 1]</c>.</param>
    /// <returns>The outcome.</returns>
    internal bool Chance(double p) => NextDouble() < p;

    /// <summary>A uniform pick from a list.</summary>
    /// <typeparam name="T">Element type.</typeparam>
    /// <param name="items">The list; must not be empty.</param>
    /// <returns>The chosen element.</returns>
    internal T Pick<T>(IReadOnlyList<T> items) => items[Int(0, items.Count)];

    /// <summary>
    /// A weighted pick. This is what keeps the generated population looking like play rather than
    /// like a uniform sweep of the taxonomy: ordinary flights are common and records are rare
    /// because the weights say so.
    /// </summary>
    /// <typeparam name="T">Element type.</typeparam>
    /// <param name="items">Choices and their relative weights.</param>
    /// <returns>The chosen element.</returns>
    internal T Weighted<T>(IReadOnlyList<(T Item, double Weight)> items)
    {
        double total = 0;
        foreach ((T _, double weight) in items)
            total += weight;

        double roll = NextDouble() * total;
        foreach ((T item, double weight) in items)
        {
            roll -= weight;
            if (roll <= 0)
                return item;
        }

        return items[^1].Item;
    }

    /// <summary>
    /// A standard normal draw (Box–Muller), for the quantities where a bell is the honest shape:
    /// touchdown speeds, apoapses, crew counts.
    /// </summary>
    /// <returns>The value.</returns>
    internal double Gaussian()
    {
        double u1 = Math.Max(NextDouble(), 1e-12);
        double u2 = NextDouble();
        return Math.Sqrt(-2.0 * Math.Log(u1)) * Math.Cos(2.0 * Math.PI * u2);
    }

    /// <summary>A normal draw clamped to a range — the shape most flight parameters want.</summary>
    /// <param name="mean">Distribution mean.</param>
    /// <param name="sigma">Standard deviation.</param>
    /// <param name="lo">Lower clamp.</param>
    /// <param name="hi">Upper clamp.</param>
    /// <returns>The value.</returns>
    internal double Normal(double mean, double sigma, double lo, double hi)
        => Math.Clamp(mean + (sigma * Gaussian()), lo, hi);

    private static ulong Mix(ulong z)
    {
        z = (z ^ (z >> 30)) * 0xBF58_476D_1CE4_E5B9UL;
        z = (z ^ (z >> 27)) * 0x94D0_49BB_1331_11EBUL;
        return z ^ (z >> 31);
    }
}

/// <summary>
/// A rolling hash of the event stream one player produced, so two runs with the same seed can be
/// compared by one 16-character string instead of by eye.
/// </summary>
/// <remarks>
/// <para>
/// It covers the <b>type</b> and the <b>sim instant</b> of every envelope, in order — everything
/// the detector, the window accumulator and the impact correlator decide — and deliberately not
/// the ULIDs. Event, flight and session ids are minted from the clock and a CSPRNG and differ on
/// every run by design; hashing them would make every digest unique and the check worthless.
/// </para>
/// <para>
/// Per-player digests are combined by sorting and re-hashing, so the digest is independent of the
/// order in which players happened to finish.
/// </para>
/// </remarks>
internal sealed class StreamDigest
{
    private readonly IncrementalHash _hash = IncrementalHash.CreateHash(HashAlgorithmName.SHA256);

    /// <summary>Folds one event into the digest.</summary>
    /// <param name="type">The event type name.</param>
    /// <param name="simT">The event's sim instant.</param>
    internal void Add(string type, double simT)
    {
        _hash.AppendData(Encoding.UTF8.GetBytes(type));
        // Rounded to the millisecond: sim instants are computed in doubles from the same inputs
        // every run, but pinning the full 17 digits of a double would make the digest hostage to
        // a floating-point reassociation the JIT is free to make.
        _hash.AppendData(BitConverter.GetBytes((long)Math.Round(simT * 1000.0)));
    }

    /// <summary>The digest, as 16 lowercase hex characters.</summary>
    /// <returns>The value.</returns>
    internal string Value()
    {
        byte[] digest = _hash.GetCurrentHash();
        var text = new StringBuilder(16);
        for (int i = 0; i < 8; i++)
            text.Append(digest[i].ToString("x2", CultureInfo.InvariantCulture));
        return text.ToString();
    }

    /// <summary>Combines per-player digests into one, independently of completion order.</summary>
    /// <param name="digests">The per-player digests.</param>
    /// <returns>The combined digest.</returns>
    internal static string Combine(IEnumerable<string> digests)
    {
        var sorted = new List<string>(digests);
        sorted.Sort(StringComparer.Ordinal);

        using IncrementalHash hash = IncrementalHash.CreateHash(HashAlgorithmName.SHA256);
        foreach (string digest in sorted)
            hash.AppendData(Encoding.UTF8.GetBytes(digest));

        byte[] value = hash.GetHashAndReset();
        var text = new StringBuilder(16);
        for (int i = 0; i < 8; i++)
            text.Append(value[i].ToString("x2", CultureInfo.InvariantCulture));
        return text.ToString();
    }
}
