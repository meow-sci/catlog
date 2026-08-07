using System;
using System.IO;
using MeowSci.Catlog.Lib.Util;

namespace MeowSci.Catlog;

/// <summary>
/// Where catlog keeps its files. Everything the mod writes lives under the KSA userland mods
/// directory, never next to the installed DLLs — a mod update replaces the install folder, and the
/// player's outbox, credential and settings must survive that.
/// </summary>
/// <remarks>
/// The layout follows the <c>ksa</c> skill's persistence rule: the folder is named for the mod's
/// kebab-case name, under <c>My Documents/My Games/Kitten Space Agency/mods/</c>.
/// </remarks>
public static class ModPaths
{
    /// <summary>The mod's folder name, matching <c>mod.toml</c>.</summary>
    public const string ModName = "catlog";

    /// <summary>The directory holding <c>catlog.toml</c>, the outbox and the install id.</summary>
    public static string DataDir { get; } = Path.Combine(
        Environment.GetFolderPath(Environment.SpecialFolder.MyDocuments),
        "My Games",
        "Kitten Space Agency",
        "mods",
        ModName);

    /// <summary>The player-editable settings file.</summary>
    public static string ConfigFile => Path.Combine(DataDir, "catlog.toml");

    /// <summary>The SQLite write-ahead outbox.</summary>
    public static string OutboxFile => Path.Combine(DataDir, "outbox.db");

    /// <summary>Where an unconfigured <c>credential_path</c> looks by default.</summary>
    public static string DefaultCredentialFile => Path.Combine(DataDir, "catlog-credential.json");

    /// <summary>The file holding this install's ULID.</summary>
    public static string InstallIdFile => Path.Combine(DataDir, "install-id.txt");

    /// <summary>
    /// The install ULID: stable for this machine across sessions, and the salt that makes every
    /// <c>kid</c> incomparable between installs (§4.2). Minted and persisted on first run.
    /// </summary>
    /// <remarks>
    /// Never throws. If the file cannot be read or written the mod runs with an ephemeral id — a
    /// per-session id is degraded (kitten identity does not join across sessions) but it is not a
    /// reason to refuse to collect anything.
    /// </remarks>
    /// <returns>The install ULID.</returns>
    public static string LoadOrCreateInstallId()
    {
        try
        {
            if (File.Exists(InstallIdFile))
            {
                string existing = File.ReadAllText(InstallIdFile).Trim();
                if (Ids.IsUlid(existing))
                    return existing;
                ModLog.Log.Warn(
                    $"catlog: '{InstallIdFile}' does not contain a ULID; minting a new install id.");
            }

            string minted = Ids.NewUlid();
            Directory.CreateDirectory(DataDir);
            AtomicWrite(InstallIdFile, minted);
            return minted;
        }
        catch (Exception ex) when (ex is IOException or UnauthorizedAccessException or NotSupportedException
                                       or ArgumentException)
        {
            string ephemeral = Ids.NewUlid();
            ModLog.Log.Warn(
                $"catlog: could not persist the install id ({ex.Message}); using a session-only id. "
                + "Kitten identity will not join across sessions.");
            return ephemeral;
        }
    }

    /// <summary>Creates <see cref="DataDir"/> if it does not exist. Never throws.</summary>
    /// <returns>True when the directory exists afterwards.</returns>
    public static bool EnsureDataDir()
    {
        try
        {
            Directory.CreateDirectory(DataDir);
            return true;
        }
        catch (Exception ex) when (ex is IOException or UnauthorizedAccessException or NotSupportedException
                                       or ArgumentException)
        {
            ModLog.Log.Error($"catlog: could not create the data directory '{DataDir}': {ex.Message}", ex);
            return false;
        }
    }

    private static void AtomicWrite(string path, string contents)
    {
        string tmp = path + ".tmp";
        File.WriteAllText(tmp, contents);
        File.Move(tmp, path, overwrite: true);
    }
}
