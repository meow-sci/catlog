using System.Linq;
using MeowSci.Catlog.Lib.Detect;
using MeowSci.Catlog.Lib.Events;
using MeowSci.Catlog.Lib.Util;
using Xunit;

namespace MeowSci.Catlog.Lib.Tests.Detect;

/// <summary>
/// §4.1 <c>career</c>: the envelope key that makes <c>sim_t</c> mean "seconds since this save's
/// game started". The lifecycle it has to survive is the one the game imposes — a session is minted
/// at every save load, so one career is many sessions — plus the one case that is genuinely
/// ambiguous, a clock that goes backwards.
/// </summary>
public sealed class CareerTests
{
    [Fact]
    public void CareerId_IsSixteenCrockfordCharactersAndDeterministic()
    {
        string a = Ids.CareerId(TestData.InstallId, "save:apollo");
        string b = Ids.CareerId(TestData.InstallId, "save:apollo");

        Assert.Equal(a, b);
        Assert.Equal(16, a.Length);
        Assert.True(Ids.IsHash16(a), $"'{a}' should be 16 lowercase Crockford characters");
    }

    /// <summary>
    /// Two saves must never collide, and neither must two installs — the server must not be able to
    /// tell that two players both called a save "apollo".
    /// </summary>
    [Fact]
    public void CareerId_SeparatesSavesAndInstalls()
    {
        Assert.NotEqual(
            Ids.CareerId(TestData.InstallId, "save:apollo"),
            Ids.CareerId(TestData.InstallId, "save:gemini"));
        Assert.NotEqual(
            Ids.CareerId(TestData.InstallId, "save:apollo"),
            Ids.CareerId("01J9V5M3E8Z0OTHERINSTALL1", "save:apollo"));
    }

    [Fact]
    public void IsHash16_RejectsTheCharactersCrockfordExcludes()
    {
        // i, l, o and u are excluded from the alphabet so nothing is visually
        // confusable with 1, 0 or v.
        Assert.False(Ids.IsHash16("iiiiiiiiiiiiiiii"));
        Assert.False(Ids.IsHash16("llllllllllllllll"));
        Assert.False(Ids.IsHash16("oooooooooooooooo"));
        Assert.False(Ids.IsHash16("uuuuuuuuuuuuuuuu"));
        Assert.False(Ids.IsHash16("ABCDEFGH01234567"));
        Assert.False(Ids.IsHash16("0123456789abcde"));  // 15
        Assert.False(Ids.IsHash16("0123456789abcdefg")); // 17
        Assert.False(Ids.IsHash16(null));
        Assert.True(Ids.IsHash16("0123456789abcdef"));
    }

    [Fact]
    public void EveryEnvelopeCarriesTheCareer()
    {
        EventPipeline pipeline = TestData.Pipeline();

        var envelopes = pipeline.ProcessSignal(TestData.Created()).ToList();
        envelopes.Add(pipeline.SessionStarted(0, TestData.WallMs));
        envelopes.AddRange(pipeline.ProcessSignal(TestData.Rud()));

        Assert.NotEmpty(envelopes);
        Assert.All(envelopes, e => Assert.Equal(TestData.CareerId, e.Career));
    }

    [Fact]
    public void NoCareerGiven_DerivesAStablePerInstallOne()
    {
        // The load harness and the conformance tests have no concept of a save.
        // They still get a career, and it is the same one every time.
        var a = new EventPipeline(new EventPipelineOptions(TestData.InstallId));
        var b = new EventPipeline(new EventPipelineOptions(TestData.InstallId));

        Assert.Equal(a.CareerId, b.CareerId);
        Assert.True(Ids.IsHash16(a.CareerId));
    }

    /// <summary>
    /// The lifecycle that matters: a save load rotates the session but the career only changes when
    /// a <i>different</i> save was loaded. Reloading the same save keeps the career, which is what
    /// makes its <c>sim_t</c> values comparable with the ones before the load.
    /// </summary>
    [Fact]
    public void SessionBoundary_RotatesTheSessionAndCarriesOrChangesTheCareer()
    {
        EventPipeline pipeline = TestData.Pipeline();
        string firstSession = pipeline.SessionId;

        // Reloading the same save: new session, same career.
        EventEnvelope reload = pipeline.ProcessSignal(
            Loaded(simT: 4_000, career: TestData.CareerId)).Single(static e => e.Type == EventTypes.SessionStarted);
        Assert.NotEqual(firstSession, reload.Session);
        Assert.Equal(TestData.CareerId, reload.Career);
        Assert.Equal(TestData.CareerId, pipeline.CareerId);

        // Loading a different save: new session and a new career.
        EventEnvelope other = pipeline.ProcessSignal(
            Loaded(simT: 12.5, career: TestData.OtherCareerId)).Single(static e => e.Type == EventTypes.SessionStarted);
        Assert.NotEqual(reload.Session, other.Session);
        Assert.Equal(TestData.OtherCareerId, other.Career);
        Assert.Equal(TestData.OtherCareerId, pipeline.CareerId);
    }

    /// <summary>
    /// A session boundary the game project could not attribute to a save keeps the career it had.
    /// Null means "I do not know", and inventing a career id there would split one save's history
    /// in two and silently lose its earlier milestone times.
    /// </summary>
    [Fact]
    public void SessionBoundaryWithNoCareer_KeepsTheCurrentOne()
    {
        EventPipeline pipeline = TestData.Pipeline();

        EventEnvelope started = pipeline.ProcessSignal(
            new SessionLoadedSignal(0, TestData.WallMs, "2026.8.5.5168", "0.1.0", System: TestData.SystemSurvey()))
            .Single(static e => e.Type == EventTypes.SessionStarted);

        Assert.Equal(TestData.CareerId, started.Career);
    }

    /// <summary>
    /// The backwards-clock case, from the mod's side. Loading an earlier save of the *same* career
    /// is emitted exactly like any other load — the mod does not judge it, it just reports a
    /// <c>session.started</c> whose <c>sim_t</c> is below what it had been emitting. That pair,
    /// same career and a lower clock, is the whole of the server's rewind rule (docs/events.md).
    /// </summary>
    [Fact]
    public void LoadingAnEarlierSave_KeepsTheCareerAndReportsTheLowerClock()
    {
        EventPipeline pipeline = TestData.Pipeline();
        pipeline.ProcessSignal(TestData.Created(simT: 0));

        EventEnvelope late = pipeline.ProcessSignal(
            Loaded(simT: 9_000, career: TestData.CareerId)).Single(static e => e.Type == EventTypes.SessionStarted);
        EventEnvelope early = pipeline.ProcessSignal(
            Loaded(simT: 1_200, career: TestData.CareerId)).Single(static e => e.Type == EventTypes.SessionStarted);

        Assert.Equal(late.Career, early.Career);
        Assert.True(early.SimT < late.SimT, "the reloaded save's clock must be reported as it is");
        Assert.NotEqual(late.Session, early.Session);
    }

    /// <summary>
    /// A career is not a session: events emitted long after a load still carry the career the load
    /// established, so the server can measure a milestone against the career's start rather than
    /// against whenever the player last quit.
    /// </summary>
    [Fact]
    public void CareerOutlivesTheSession()
    {
        EventPipeline pipeline = TestData.Pipeline();
        pipeline.ProcessSignal(Loaded(simT: 0, career: TestData.OtherCareerId));

        pipeline.ProcessSignal(TestData.Created(simT: 10));
        EventEnvelope rud = pipeline.ProcessSignal(TestData.Rud(simT: 500))
            .Single(e => e.Type == EventTypes.VehicleRud);

        Assert.Equal(TestData.OtherCareerId, rud.Career);
        Assert.Equal(500, rud.SimT);
    }

    [Fact]
    public void NdjsonLine_CarriesCareerAsAnEnvelopeKey()
    {
        string line = TestData.Envelope(career: TestData.CareerId).ToNdjsonLine();

        Assert.Contains($"\"career\":\"{TestData.CareerId}\"", line);
    }

    private static SessionLoadedSignal Loaded(double simT, string career)
        => new(simT, TestData.WallMs, "2026.8.5.5168", "0.1.0", career, TestData.SystemSurvey());
}
