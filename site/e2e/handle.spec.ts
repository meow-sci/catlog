import { expect, test, type Request } from "@playwright/test";
import {
  claimExpectingError,
  claimHandle,
  decodeJws,
  IDP_USERS,
  signInToDashboard,
  thumbprintOfCredential,
} from "./helpers";

/**
 * §8 spec 2 — the credential wizard.
 *
 * The claim happens once, in the first test, and the rest of the file asserts
 * against it. That is deliberate: §4.7 makes a handle permanent and never
 * recycled, so "claim it again in a fresh test" is not a thing the system
 * allows, and the failure modes being tested (taken, case-collision) only exist
 * because the first claim succeeded.
 */

const HANDLE = "e2e_whiskers";

test.describe.configure({ mode: "serial" });

test.describe("the credential wizard", () => {
  test("claims a handle and downloads a credential whose key matches the license", async ({ page }) => {
    await signInToDashboard(page, IDP_USERS.discordAged);

    // Capture what actually crosses the network. This is the assertion the
    // whole design exists for: the private key must never leave the page.
    const posted: string[] = [];
    page.on("request", (req: Request) => {
      if (req.method() === "POST" && new URL(req.url()).pathname === "/api/handles") {
        posted.push(req.postData() ?? "");
      }
    });

    const { credential, jkt, filename } = await claimHandle(page, HANDLE);

    // --- what was sent -------------------------------------------------------
    expect(posted).toHaveLength(1);
    const body = JSON.parse(posted[0]);
    expect(body.handle).toBe(HANDLE);
    expect(Object.keys(body).sort()).toEqual(["handle", "jwk"]);
    // Exactly the four §4.5.2 members, and above all no `d`.
    expect(Object.keys(body.jwk).sort()).toEqual(["crv", "kty", "x", "y"]);
    expect(body.jwk).not.toHaveProperty("d");
    expect(body.jwk.kty).toBe("EC");
    expect(body.jwk.crv).toBe("P-256");
    // Belt and braces: the private key material must not appear anywhere in the
    // request, under any key name.
    expect(posted[0]).not.toContain("PRIVATE KEY");

    // --- what was downloaded --------------------------------------------------
    expect(filename).toBe("catlog-credential.json");
    expect(credential.format).toBe(1);
    expect(credential.handle).toBe(HANDLE);
    expect(credential.private_key_pem).toContain("-----BEGIN PRIVATE KEY-----");
    expect(credential.private_key_pem).toContain("-----END PRIVATE KEY-----");

    // --- the license ----------------------------------------------------------
    const { header, claims } = decodeJws(credential.license);
    expect(header.alg).toBe("ES256");
    expect(header.typ).toBe("catlog-license+jwt");
    expect(header.kid).toMatch(/^catlog-\d{6}$/);
    expect(claims.handle).toBe(HANDLE);
    expect(claims.iss).toBe("http://127.0.0.1:8080");
    expect(claims.ver).toBe(1);
    expect(claims.jti).toMatch(/^lic_/);
    expect(claims.exp - claims.iat).toBe(180 * 24 * 60 * 60);
    expect(typeof claims.sub).toBe("string");

    // --- the binding ----------------------------------------------------------
    // Recompute the RFC 7638 thumbprint from the downloaded private key, in the
    // page, with the same WebCrypto that generated it.
    const recomputed = await thumbprintOfCredential(page, credential.private_key_pem);
    expect(claims.cnf.jkt).toBe(recomputed);
    expect(jkt).toBe(recomputed);

    // The page says so too, in the words a player has to act on.
    await expect(page.locator("#wizard-warning")).toContainText(/cannot be downloaded again/i);
    await expect(page.locator("#wizard-claimed")).toHaveText(HANDLE);
  });

  test("the handle now appears on the dashboard and has a public profile", async ({ page }) => {
    await signInToDashboard(page, IDP_USERS.discordAged);
    const handleCard = page.locator(`.handle[data-handle="${HANDLE}"]`);
    await expect(handleCard).toBeVisible();
    await expect(handleCard.locator(".credential[data-jkt]")).toHaveCount(1);

    await page.goto(`/p/${HANDLE}`);
    await expect(page.locator("#profile-handle")).toHaveText(HANDLE);
  });

  test("a duplicate claim is handle_taken", async ({ page }) => {
    await signInToDashboard(page, IDP_USERS.discordAged);
    expect(await claimExpectingError(page, HANDLE)).toBe("handle_taken");
  });

  test("a case-collision is handle_taken too", async ({ page }) => {
    await signInToDashboard(page, IDP_USERS.discordAged);
    expect(await claimExpectingError(page, HANDLE.toUpperCase())).toBe("handle_taken");
  });

  test("a reserved word is handle_reserved", async ({ page }) => {
    await signInToDashboard(page, IDP_USERS.discordAged);
    expect(await claimExpectingError(page, "admin")).toBe("handle_reserved");
  });

  test("a malformed handle is handle_invalid", async ({ page }) => {
    await signInToDashboard(page, IDP_USERS.discordAged);

    // Each of these fails §4.7's regex: non-ASCII, a leading separator, a
    // trailing separator, an interior space. The input carries maxlength=150,
    // so "151 characters" cannot be typed at all and is asserted against the
    // API below instead.
    expect(await claimExpectingError(page, "wískers")).toBe("handle_invalid");
    expect(await claimExpectingError(page, "_leading")).toBe("handle_invalid");
    expect(await claimExpectingError(page, "trailing-")).toBe("handle_invalid");
    expect(await claimExpectingError(page, "has space")).toBe("handle_invalid");

    // The wizard mirrors §4.7's regex so an obvious typo costs no round trip;
    // the server is still the authority, so assert its answer directly too.
    // (`page.request` carries the browser context's session cookie.)
    const res = await page.request.post("/api/handles", {
      data: {
        handle: "a".repeat(151),
        jwk: { kty: "EC", crv: "P-256", x: "f83OJ3D2xF1Bg8vub9tLe1gHMzV76e8Tus9uPHvRVEU", y: "x_FEzRu9m36HLN_tue659LNpXW6pCyStikYjKIWI5a0" },
      },
    });
    expect(res.status()).toBe(400);
    expect(await res.json()).toMatchObject({ error: "handle_invalid" });
  });

  test("the server refuses a JWK carrying a private key", async ({ page }) => {
    // The wizard cannot produce this, by construction. The server must refuse it
    // anyway: it is the mistake that would be catastrophic if it ever landed.
    await signInToDashboard(page, IDP_USERS.discordAged);
    const res = await page.request.post("/api/handles", {
      data: {
        handle: "e2e_private_key_leak",
        jwk: {
          kty: "EC",
          crv: "P-256",
          x: "f83OJ3D2xF1Bg8vub9tLe1gHMzV76e8Tus9uPHvRVEU",
          y: "x_FEzRu9m36HLN_tue659LNpXW6pCyStikYjKIWI5a0",
          d: "jpsQnnGQmL-YBIffH1136cspYG6-0iY7X1fCE9-E9LI",
        },
      },
    });
    expect(res.status()).toBe(400);
    expect(await res.json()).toMatchObject({ error: "bad_request" });
  });
});
