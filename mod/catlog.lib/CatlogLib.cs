namespace MeowSci.Catlog.Lib;

/// <summary>
/// Assembly marker for <c>MeowSci.Catlog.Lib</c>.
/// </summary>
/// <remarks>
/// WP6 re-anchored the assembly guard test (INITIAL_IMPL_PLAN §7.1) onto
/// <see cref="Events.EventEnvelope"/>, as the plan specifies — the guard now follows a type the
/// library genuinely needs. This marker survives only because <c>catlog.sim</c> and
/// <c>catlog.integration.tests</c> still reference it from their WP0 placeholders; WP7 replaces
/// both with real code, and this type can be deleted at that point.
/// </remarks>
public static class CatlogLib
{
    /// <summary>Simple name of this assembly, as it appears in metadata.</summary>
    public const string AssemblyName = "MeowSci.Catlog.Lib";
}
