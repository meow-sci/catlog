using System;

namespace MeowSci.Catlog.Lib.Util;

/// <summary>
/// Log sink. <c>catlog.lib</c> never writes to the console directly: the game project installs
/// a StarMap/Brutal sink, <c>catlog.sim</c> installs a console sink, and the tests install a
/// null sink (the dead-latch pattern deliberately provokes faults).
/// </summary>
public interface IModLogger
{
    /// <summary>Per-item faults that are expected and recoverable.</summary>
    /// <param name="message">Message to log.</param>
    void Debug(string message);

    /// <summary>Lifecycle milestones.</summary>
    /// <param name="message">Message to log.</param>
    void Info(string message);

    /// <summary>A capability degraded but the session continues.</summary>
    /// <param name="message">Message to log.</param>
    void Warn(string message);

    /// <summary>A subsystem is now permanently off for the session.</summary>
    /// <param name="message">Message to log.</param>
    /// <param name="exception">The fault, when there is one.</param>
    void Error(string message, Exception? exception = null);
}

/// <summary>
/// The ambient logger. Copied from <c>gatOS/gatOS.Logging/ModLog.cs</c> (the abstraction is what
/// lets a KSA-free assembly log at all, and what lets tests silence it).
/// </summary>
public static class ModLog
{
    private static IModLogger _log = new ConsoleLogger();

    /// <summary>The installed sink. Never null.</summary>
    public static IModLogger Log => _log;

    /// <summary>Installs a sink (the host does this once at load).</summary>
    /// <param name="logger">The sink to install.</param>
    public static void SetLogger(IModLogger logger)
        => _log = logger ?? throw new ArgumentNullException(nameof(logger));

    /// <summary>Restores the default console sink. Primarily a test hook.</summary>
    public static void ResetToDefault() => _log = new ConsoleLogger();

    /// <summary>The fallback sink: writes <c>catlog [LVL]: …</c> to stdout.</summary>
    private sealed class ConsoleLogger : IModLogger
    {
        public void Debug(string message) => Console.WriteLine($"catlog [DBG]: {message}");

        public void Info(string message) => Console.WriteLine($"catlog [INF]: {message}");

        public void Warn(string message) => Console.WriteLine($"catlog [WRN]: {message}");

        public void Error(string message, Exception? exception = null)
            => Console.WriteLine(exception is null
                ? $"catlog [ERR]: {message}"
                : $"catlog [ERR]: {message}: {exception}");
    }
}
