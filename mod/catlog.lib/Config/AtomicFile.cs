using System.IO;

namespace MeowSci.Catlog.Lib.Config;

/// <summary>
/// Crash-safe file writes: content is written to a sibling temp file and renamed over the
/// destination, so an interrupted write can never leave a truncated file behind.
/// </summary>
/// <remarks>Copied from <c>gatOS/gatOS.GameMod/Configuration/AtomicFile.cs</c>.</remarks>
public static class AtomicFile
{
    /// <summary>Writes text atomically.</summary>
    /// <param name="path">Destination path.</param>
    /// <param name="contents">The text to write.</param>
    public static void WriteAllText(string path, string contents)
    {
        string temp = path + ".tmp";
        File.WriteAllText(temp, contents);
        File.Move(temp, path, overwrite: true);
    }
}
