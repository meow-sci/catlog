using System;
using System.Collections.Generic;

namespace MeowSci.Catlog.Sim.Scenarios;

/// <summary>The six canonical scenarios of §7.3 (docs/mod.md), in their canonical order.</summary>
public static class ScenarioCatalog
{
    private static readonly IScenario[] Scenarios =
    [
        new HopLithobrakeScenario(),
        new OrbitAndBackScenario(),
        new RudSamplerScenario(),
        new TumbleweedScenario(),
        new CheaterScenario(),
        new SoakScenario(),
    ];

    /// <summary>Every scenario.</summary>
    public static IReadOnlyList<IScenario> All => Scenarios;

    /// <summary>Looks a scenario up by name.</summary>
    /// <param name="name">The CLI name.</param>
    /// <returns>The scenario.</returns>
    /// <exception cref="SimException">No scenario has that name.</exception>
    public static IScenario Find(string name)
    {
        foreach (IScenario scenario in Scenarios)
        {
            if (string.Equals(scenario.Name, name, StringComparison.OrdinalIgnoreCase))
                return scenario;
        }

        var names = new List<string>(Scenarios.Length);
        foreach (IScenario scenario in Scenarios)
            names.Add(scenario.Name);
        throw new SimException($"no scenario named '{name}'; known scenarios: {string.Join(", ", names)}");
    }
}
