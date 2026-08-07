import { defineConfig, devices } from "@playwright/test";

/**
 * catlog end-to-end suite (INITIAL_IMPL_PLAN §8).
 *
 * Everything runs against a local catlogd and a local mockidp — no external
 * network, ever (D2). WebCrypto works because `http://127.0.0.1` is a secure
 * context, so the credential wizard needs no TLS.
 *
 * Two modes:
 *
 *  - `make e2e` (default) lets playwright start both servers. `server-run-test-env`
 *    wipes a throwaway data directory, starts catlogd on it and seeds the demo
 *    dataset; `mockidp-run` starts the IdP simulator.
 *  - `CATLOG_E2E_EXTERNAL=1` skips the webServer block entirely and drives an
 *    instance somebody else started. `scripts/e2e-full.sh` uses this to run
 *    `boards.spec.ts` against the same server the simulator just flew into,
 *    which is the whole point of that proof.
 */
const external = !!process.env.CATLOG_E2E_EXTERNAL;

export const BASE_URL = process.env.CATLOG_E2E_BASE_URL ?? "http://127.0.0.1:8080";
export const ADMIN_URL = process.env.CATLOG_E2E_ADMIN_URL ?? "http://127.0.0.1:6060";
export const MOCKIDP_URL = process.env.CATLOG_E2E_MOCKIDP_URL ?? "http://127.0.0.1:9090";

export default defineConfig({
  testDir: ".",
  outputDir: ".results",
  fullyParallel: false,
  // One worker. The suite drives a single stateful server: handle claims, bans
  // and account deletions are global facts about it, and two workers racing over
  // the same handle namespace would fail in ways that have nothing to do with
  // the code under test.
  workers: 1,
  forbidOnly: !!process.env.CI,
  retries: 0,
  reporter: [["list"], ["html", { outputFolder: ".report", open: "never" }]],

  use: {
    baseURL: BASE_URL,
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
    acceptDownloads: true,
    // Deterministic viewport so a "not visible" failure means what it says.
    viewport: { width: 1280, height: 900 },
  },

  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],

  webServer: external
    ? undefined
    : [
        {
          command: "make -C ../.. server-run-test-env",
          url: `${BASE_URL}/healthz`,
          reuseExistingServer: false,
          stdout: "pipe",
          stderr: "pipe",
          timeout: 60_000,
        },
        {
          command: "make -C ../.. mockidp-run",
          url: `${MOCKIDP_URL}/healthz`,
          reuseExistingServer: false,
          stdout: "pipe",
          stderr: "pipe",
          timeout: 60_000,
        },
      ],
});
