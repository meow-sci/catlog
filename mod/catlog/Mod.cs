using System;
using MeowSci.Catlog.Lib.Util;
using StarMap.API;

namespace MeowSci.Catlog;

/// <summary>
/// The StarMap entry point. Owns <see cref="CatlogRuntime"/> for the life of the game process and
/// nothing else: every lifecycle hook is a guarded one-liner into the runtime, the patcher or the
/// status window.
/// </summary>
/// <remarks>
/// <para>
/// Lifecycle, per the <c>ksa</c> skill: nothing happens at immediate-load (the renderer is not
/// live); everything is built in <c>[StarMapAllModsLoaded]</c>; the per-frame sample and frame
/// boundary run in <c>[StarMapBeforeGui]</c>; the ImGui window draws in <c>[StarMapAfterGui]</c>;
/// the runtime is drained and disposed in <c>[StarMapUnload]</c>.
/// </para>
/// <para>
/// Every hook body is wrapped. A catlog failure must never break game init, a frame, or shutdown —
/// this mod is a passive observer, and the worst thing it could do is cost someone a flight because
/// it threw inside the frame loop.
/// </para>
/// </remarks>
[StarMapMod]
public sealed class Mod
{
    private readonly StatusWindow _window = new();

    private CatlogRuntime? _runtime;
    private string _initError = string.Empty;
    private bool _uiDead;

    /// <summary>StarMap contract: catlog unloads in the normal (late) phase.</summary>
    public bool ImmediateUnload => false;

    /// <summary>The renderer is not live yet — nothing to do.</summary>
    [StarMapImmediateLoad]
    public void OnImmediateLoad()
    {
    }

    /// <summary>Builds the runtime and installs the Harmony patch table.</summary>
    [StarMapAllModsLoaded]
    public void OnFullyLoaded()
    {
        try
        {
            _runtime = CatlogRuntime.Create();
            ModLog.SetLogger(new LevelFilteredLogger(_runtime.Config.LogLevel));

            // The patcher is installed even when the outbox failed to open: the status window then
            // reports a dead subsystem rather than an inert mod with no explanation, and
            // CatlogRuntime.Signal is a no-op while collection is off, so the patch bodies cost a
            // boolean each.
            Patcher.Patch(_runtime);
        }
        catch (Exception ex)
        {
            _initError = ex.Message;
            ModLog.Log.Error("catlog: initialization failed; the mod is inactive for this session.", ex);
        }
    }

    /// <summary>
    /// The per-frame game-thread pass. Runs after <c>Program.PrepareFrame</c> has applied the
    /// solver batch and the input events, which is what makes it a safe frame boundary — see
    /// <see cref="CatlogRuntime.Tick"/>.
    /// </summary>
    /// <param name="dt">Frame delta in seconds.</param>
    [StarMapBeforeGui]
    public void OnBeforeUi(double dt)
    {
        try
        {
            _runtime?.Tick(dt);
        }
        catch (Exception ex)
        {
            // Tick already swallows its own faults; reaching here means something structural (a
            // type-load failure), which will recur every frame. Log once via the runtime's latch.
            ModLog.Log.Debug($"catlog: the frame tick faulted: {ex.Message}");
        }
    }

    /// <summary>Draws the status window. F10 toggles it.</summary>
    /// <param name="dt">Frame delta in seconds.</param>
    [StarMapAfterGui]
    public void OnAfterUi(double dt)
    {
        if (_uiDead)
            return;

        try
        {
            _window.Draw(_runtime, _initError);
        }
        catch (Exception ex)
        {
            // A UI that cannot be entered at all would otherwise throw — and spam — every frame.
            _uiDead = true;
            ModLog.Log.Error($"catlog: the status window is disabled after a draw error: {ex.Message}");
        }
    }

    /// <summary>Removes the patches, drains the worker and ships what it can.</summary>
    [StarMapUnload]
    public void Unload()
    {
        try
        {
            // Patches first: the runtime is about to stop accepting signals, and a patch body
            // running against a disposed runtime would be a use-after-free in all but name.
            Patcher.Unload();
            _runtime?.Dispose();
            _runtime = null;
        }
        catch (Exception ex)
        {
            ModLog.Log.Error($"catlog: unload error: {ex.Message}", ex);
        }
        finally
        {
            ModLog.ResetToDefault();
        }
    }

    /// <summary>
    /// Applies the configured <c>log_level</c> to the console sink. <c>catlog.lib</c> logs
    /// unconditionally through <see cref="ModLog"/>; the filter belongs at the sink, which is the
    /// only place that knows the player's setting.
    /// </summary>
    private sealed class LevelFilteredLogger : IModLogger
    {
        private readonly int _minimum;

        internal LevelFilteredLogger(string level) => _minimum = level switch
        {
            "debug" => 0,
            "warn" => 2,
            "error" => 3,
            _ => 1, // info
        };

        public void Debug(string message)
        {
            if (_minimum <= 0)
                Console.WriteLine($"catlog [DBG]: {message}");
        }

        public void Info(string message)
        {
            if (_minimum <= 1)
                Console.WriteLine($"catlog [INF]: {message}");
        }

        public void Warn(string message)
        {
            if (_minimum <= 2)
                Console.WriteLine($"catlog [WRN]: {message}");
        }

        public void Error(string message, Exception? exception = null)
        {
            Console.WriteLine($"catlog [ERR]: {message}");
            if (exception is not null)
                Console.WriteLine(exception);
        }
    }
}
