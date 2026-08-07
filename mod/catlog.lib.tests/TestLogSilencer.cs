using System;
using System.Runtime.CompilerServices;
using MeowSci.Catlog.Lib.Util;

namespace MeowSci.Catlog.Lib.Tests;

/// <summary>
/// Routes <see cref="ModLog"/> away from the console for the whole test assembly. The dead-latch
/// pattern logs on every fault and the detector/outbox/shipper tests deliberately provoke faults,
/// so without this the run is unreadable.
/// </summary>
/// <remarks>
/// A module initializer rather than an xunit fixture: it runs once, before any test class is
/// constructed, and needs no collection ceremony. (gatOS uses NUnit's <c>[SetUpFixture]</c> for the
/// same job — see docs/DECISIONS.md on the framework divergence.)
/// </remarks>
internal static class TestLogSilencer
{
    [ModuleInitializer]
    internal static void Silence() => ModLog.SetLogger(new NullLogger());

    private sealed class NullLogger : IModLogger
    {
        public void Debug(string message)
        {
        }

        public void Info(string message)
        {
        }

        public void Warn(string message)
        {
        }

        public void Error(string message, Exception? exception = null)
        {
        }
    }
}
