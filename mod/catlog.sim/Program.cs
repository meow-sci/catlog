using System;
using MeowSci.Catlog.Lib;

namespace MeowSci.Catlog.Sim;

/// <summary>Entry point for the catlog gameplay simulator.</summary>
public static class Program
{
    /// <summary>
    /// WP0 scaffolding: prints usage. The scenario runner (INITIAL_IMPL_PLAN §7.3) lands in WP7.
    /// </summary>
    /// <param name="args">Command-line arguments (ignored until WP7).</param>
    /// <returns>Process exit code.</returns>
    public static int Main(string[] args)
    {
        _ = args;
        Console.WriteLine($"catlog.sim — gameplay simulator over {CatlogLib.AssemblyName}");
        Console.WriteLine();
        Console.WriteLine("usage: catlog.sim --scenario <name> --server <url> --credential <path>");
        Console.WriteLine("                  [--list] [--assert] [--speed <n>]");
        Console.WriteLine();
        Console.WriteLine("not yet implemented (WP7)");
        return 0;
    }
}
