import { expect, test } from "@playwright/test";
import { IDP_USERS, signIn, signInToDashboard } from "./helpers";

/**
 * §8 spec 1 — the three OAuth flows against mockidp, and the account-age gate.
 *
 * Every test gets its own browser context, so a session never leaks from one to
 * the next: "does signing in work" and "does a refused sign-in leave a session"
 * are different questions and must not share cookies.
 */

test.describe("sign-in", () => {
  test("the login page offers all three providers and promises no email", async ({ page }) => {
    await page.goto("/login");
    await expect(page.locator("#login-discord")).toBeVisible();
    await expect(page.locator("#login-google")).toBeVisible();
    await expect(page.locator("#login-github")).toBeVisible();
    await expect(page.locator("#login-privacy")).toContainText(/never asks for your email/i);
  });

  test("discord: an aged account reaches the dashboard", async ({ page }) => {
    await signInToDashboard(page, IDP_USERS.discordAged);
    // Signed in means the navigation offers the dashboard rather than sign-in.
    await expect(page.locator("#nav-dashboard")).toBeVisible();
    await expect(page.locator("#nav-login")).toHaveCount(0);
  });

  test("google reaches the dashboard", async ({ page }) => {
    await signInToDashboard(page, IDP_USERS.google);
  });

  test("github reaches the dashboard", async ({ page }) => {
    await signInToDashboard(page, IDP_USERS.github);
  });

  test("discord: a brand-new snowflake is refused as account_too_new", async ({ page }) => {
    await signIn(page, IDP_USERS.discordNew);

    const error = page.locator("#auth-error");
    await expect(error).toBeVisible();
    await expect(error).toHaveAttribute("data-error", "account_too_new");
    await expect(page.locator("#auth-error-code")).toHaveText("account_too_new");
    await expect(page.locator("#auth-error-detail")).not.toBeEmpty();
    await expect(page.locator("#auth-error-retry")).toHaveAttribute("href", "/login");

    // A refused sign-in must leave no session behind: the dashboard has to send
    // the browser back to /login rather than render.
    await page.goto("/dashboard");
    await expect(page).toHaveURL(/\/login/);
  });

  test("github: a brand-new account is refused too", async ({ page }) => {
    await signIn(page, IDP_USERS.githubNew);
    await expect(page.locator("#auth-error")).toHaveAttribute("data-error", "account_too_new");
  });

  test("the dashboard is session-gated", async ({ page }) => {
    await page.goto("/dashboard");
    await expect(page).toHaveURL(/\/login\?next=%2Fdashboard$/);
    await expect(page.locator("#login-title")).toBeVisible();
  });

  test("the login failure answers JSON when JSON is asked for", async ({ request }) => {
    // §4.9's shape has to survive whichever renderer is installed.
    const res = await request.get("/auth/nosuchidp/start", {
      headers: { Accept: "application/json" },
    });
    expect(res.status()).toBe(404);
    expect(await res.json()).toMatchObject({ error: "not_found" });
  });
});
