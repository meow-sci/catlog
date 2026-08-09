// @ts-check
import { defineConfig } from "astro/config";
import starlight from "@astrojs/starlight";
import react from "@astrojs/react";

// https://astro.build/config
export default defineConfig({
  // Published on GitHub Pages under the meow-sci org's custom domain, served at
  // https://meow.science.fail/catlog/ (siblings gatOS and flexo live at /gatOS/
  // and /flexo/). `site` + `base` make Starlight emit correct absolute/prefixed
  // URLs; the base must match the repo name exactly (case-sensitive segment).
  site: "https://meow.science.fail",
  base: "/catlog/",
  integrations: [
    react({
      // React Compiler is on, matching spa/. Rules of React are therefore
      // mandatory and hand-written useMemo/useCallback/memo are forbidden.
      babel: { plugins: [["babel-plugin-react-compiler", {}]] },
    }),
    starlight({
      title: "catlog",
      description:
        "The player's reference for catlog: what the Kitten Space Agency mod records, and how those records become leaderboards.",
      customCss: ["./src/styles/custom.css"],
      editLink: {
        baseUrl: "https://github.com/meow-sci/catlog/edit/main/docs-site/",
      },
      social: [{ icon: "github", label: "GitHub", href: "https://github.com/meow-sci/catlog" }],
      sidebar: [
        {
          label: "Start here",
          items: [
            { label: "What catlog is", slug: "start/what-catlog-is" },
            { label: "How a flight becomes a score", slug: "start/flight-to-score" },
            { label: "Your identity and your handle", slug: "start/identity" },
            { label: "Turning things off", slug: "start/turning-things-off" },
          ],
        },
        {
          label: "What gets recorded",
          items: [
            { label: "The event catalog", slug: "events" },
            { label: "Browse every event", slug: "events/browse" },
            { label: "Sessions and flights", slug: "events/sessions-and-flights" },
            { label: "Your vehicle", slug: "events/vehicle" },
            { label: "Engines", slug: "events/engines" },
            { label: "Kittens", slug: "events/kittens" },
            { label: "Background telemetry", slug: "events/telemetry" },
          ],
        },
        {
          label: "Leaderboards",
          items: [
            { label: "How boards are built", slug: "leaderboards" },
            { label: "Every board", slug: "leaderboards/catalog" },
            { label: "What counts and what doesn't", slug: "leaderboards/eligibility" },
          ],
        },
        {
          label: "Reference",
          items: [{ autogenerate: { directory: "reference" } }],
        },
      ],
    }),
  ],
});
