namespace MeowSci.Catlog.Lib.Events;

/// <summary>Maps KSA locomotion-mode names to the lowercase values carried on the wire.</summary>
/// <remarks>
/// The input is a name rather than the KSA enum so the mapping stays in the game-free core and is
/// directly testable. The switch is deliberately total: an unreadable value or a mode introduced
/// by a future game build becomes <c>unknown</c> rather than an accidental new wire value.
/// </remarks>
public static class LocomotionModeName
{
    /// <summary>Returns the lowercase wire name for a KSA locomotion mode.</summary>
    /// <param name="mode">The enum's exact <c>ToString()</c> value, or null when unreadable.</param>
    /// <returns>The known lowercase name, or <c>unknown</c>.</returns>
    public static string FromGameName(string? mode) => mode switch
    {
        "Mmu" => "mmu",
        "Grounded" => "grounded",
        "Airborne" => "airborne",
        "Tumbling" => "tumbling",
        "Rightening" => "rightening",
        "Ladder" => "ladder",
        _ => "unknown",
    };
}
