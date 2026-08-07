import { expect, test } from "@playwright/test";
import { claimExpectingError, claimHandle, IDP_USERS, signInToDashboard } from "./helpers";

/**
 * §8 spec 5 — revoke, delete-my-data, and the permanence of a handle.
 *
 * Serial by necessity: each step is a precondition of the next, and the account
 * this file creates is deleted by the end of it.
 */

const HANDLE = "e2e_lifecycle";

test.describe.configure({ mode: "serial" });

test.describe("account lifecycle", () => {
  let jkt = "";

  test("claim a handle and see its credential on the dashboard", async ({ page }) => {
    await signInToDashboard(page, IDP_USERS.google);
    const claimed = await claimHandle(page, HANDLE);
    jkt = claimed.jkt;
    expect(jkt).not.toBe("");

    // Re-render from the server rather than trusting the wizard's own view.
    await page.goto("/dashboard");
    const card = page.locator(`.handle[data-handle="${HANDLE}"]`);
    await expect(card).toBeVisible();
    await expect(card.locator(`.credential[data-jkt="${jkt}"]`)).toHaveCount(1);

    // The handle is public the moment it exists (§5.4's directory reload).
    const profile = await page.request.get(`/p/${HANDLE}`);
    expect(profile.status()).toBe(200);
  });

  test("revoking takes the jkt out of the live list", async ({ page }) => {
    await signInToDashboard(page, IDP_USERS.google);
    const card = page.locator(`.handle[data-handle="${HANDLE}"]`);
    await expect(card.locator(`.credential[data-jkt="${jkt}"]`)).toHaveCount(1);

    // Destructive buttons arm on the first click and act on the second — there
    // is no window.confirm to intercept, on purpose.
    const revoke = card.locator("button.catlog-revoke");
    await revoke.click();
    await expect(revoke).toHaveAttribute("data-armed", "true");
    await revoke.click();
    // The handler reloads the page: the handle list is server-rendered, so
    // re-fetching it is one source of truth rather than two renderers.
    await page.waitForLoadState("domcontentloaded");

    await expect(page.locator(`.credential[data-jkt="${jkt}"]`)).toHaveCount(0);
    // It is still listed — §5.7 asks for the `revoked` field — but as a dead one.
    await expect(page.locator(`.credential-revoked[data-revoked-jkt="${jkt}"]`)).toHaveCount(1);

    // Revoking is not banning (D9): the handle and its profile survive.
    await expect(page.locator(`.handle[data-handle="${HANDLE}"]`)).toBeVisible();
    expect((await page.request.get(`/p/${HANDLE}`)).status()).toBe(200);
  });

  test("delete-my-data signs the account out and takes the profile with it", async ({ page }) => {
    await signInToDashboard(page, IDP_USERS.google);

    await page.locator("#account-danger details summary").click();
    const del = page.locator("#delete-account");
    await del.click();
    await expect(del).toHaveAttribute("data-armed", "true");
    await del.click();
    await page.waitForURL((url) => url.pathname === "/");

    // Logged out: the navigation offers sign-in again, and the dashboard
    // redirects rather than rendering.
    await expect(page.locator("#nav-login")).toBeVisible();
    await page.goto("/dashboard");
    await expect(page).toHaveURL(/\/login/);

    // The profile is gone. Unknown, retired and banned all answer 404 (§4.8).
    const profile = await page.request.get(`/p/${HANDLE}`);
    expect(profile.status()).toBe(404);
  });

  test("signing in again after a delete is refused", async ({ page }) => {
    // §4.7 keeps a tombstone, and the deny-list treats a tombstone as banned —
    // so the deleted account cannot quietly come back (A-WP3-5).
    await page.goto(`/auth/${IDP_USERS.google.idp}/start`);
    await page.locator(IDP_USERS.google.button).click();
    await expect(page.locator("#auth-error")).toHaveAttribute("data-error", "banned");
  });

  test("the handle is retired: a second account cannot claim it", async ({ page }) => {
    // A different provider, a different account, the same name. D9: a handle is
    // never recycled, so nobody can be mistaken for who used to hold it.
    await signInToDashboard(page, IDP_USERS.github);
    expect(await claimExpectingError(page, HANDLE)).toBe("handle_taken");
    expect(await claimExpectingError(page, HANDLE.toUpperCase())).toBe("handle_taken");
  });
});
