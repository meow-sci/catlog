import { expect, type APIRequestContext, type Page } from "@playwright/test";
import { ADMIN_URL } from "./playwright.config";

/**
 * Shared fixtures for the §8 suite.
 *
 * Everything here talks to the local catlogd, the local mockidp and nothing
 * else. No external host is ever contacted (D2).
 */

/**
 * The mockidp sign-in buttons. These DOM ids are a pinned contract on the
 * server side (`cmd/mockidp`'s `TestDOMIdsAreStable`) — if one of these strings
 * stops matching, that test fails too, which is the point.
 */
export const IDP_USERS = {
  /** Discord, snowflake old enough to clear the 30-day gate (§4.7). */
  discordAged: { idp: "discord", button: "#login-as-whiskers-discord-old-account" },
  /** Discord, snowflake minted now → `account_too_new`. */
  discordNew: { idp: "discord", button: "#login-as-sprocket-discord-new-account" },
  /** Google publishes no creation time, so there is no age gate (§4.7). */
  google: { idp: "google", button: "#login-as-mittens-google" },
  /** GitHub, `created_at` in 2020. */
  github: { idp: "github", button: "#login-as-clawdia-github" },
  /** GitHub, `created_at` today → `account_too_new`. */
  githubNew: { idp: "github", button: "#login-as-pixel-github-new-account" },
} as const;

/**
 * Runs a full OAuth dance against mockidp and returns on whatever catlogd
 * redirected to — the dashboard on success, the §4.9 error page otherwise.
 */
export async function signIn(page: Page, user: { idp: string; button: string }): Promise<void> {
  await page.goto(`/auth/${user.idp}/start`);
  await expect(page.locator("#mockidp-title")).toBeVisible();
  await page.locator(user.button).click();
  await page.waitForLoadState("domcontentloaded");
}

/** Signs in and asserts the dashboard is reached. */
export async function signInToDashboard(page: Page, user: { idp: string; button: string }): Promise<void> {
  await signIn(page, user);
  await expect(page).toHaveURL(/\/dashboard$/);
  await expect(page.locator("#dashboard-title")).toBeVisible();
}

/**
 * Drives the credential wizard and returns the downloaded credential file plus
 * what the page claims about it.
 *
 * The download is awaited around the click rather than after it: the wizard
 * triggers it as soon as the license arrives, and a `waitForEvent` registered
 * afterwards can miss it.
 */
export async function claimHandle(
  page: Page,
  handle: string,
): Promise<{ credential: Credential; jkt: string; filename: string }> {
  await page.locator("#wizard-handle").fill(handle);

  const [download] = await Promise.all([
    page.waitForEvent("download"),
    page.locator("#wizard-submit").click(),
  ]);

  await expect(page.locator("#wizard-step-4")).toBeVisible();
  const jkt = (await page.locator("#wizard-jkt").textContent())?.trim() ?? "";

  const stream = await download.createReadStream();
  const chunks: Buffer[] = [];
  for await (const chunk of stream) chunks.push(Buffer.from(chunk));
  const credential = JSON.parse(Buffer.concat(chunks).toString("utf8")) as Credential;

  return { credential, jkt, filename: download.suggestedFilename() };
}

/** Attempts a claim that is expected to fail, and returns the §4.9 code. */
export async function claimExpectingError(page: Page, handle: string): Promise<string> {
  await page.locator("#wizard-handle").fill(handle);
  await page.locator("#wizard-submit").click();
  const error = page.locator("#wizard-error");
  await expect(error).toBeVisible();
  await expect(error).not.toHaveAttribute("data-error", "");
  return (await error.getAttribute("data-error")) ?? "";
}

/** The §4.6 credential file. */
export interface Credential {
  format: number;
  handle: string;
  license: string;
  private_key_pem: string;
}

/** Decodes a compact JWS's header and claims without verifying the signature. */
export function decodeJws(compact: string): { header: any; claims: any } {
  const parts = compact.split(".");
  if (parts.length !== 3) throw new Error(`not a compact JWS: ${parts.length} parts`);
  const decode = (s: string) => JSON.parse(Buffer.from(s, "base64url").toString("utf8"));
  return { header: decode(parts[0]), claims: decode(parts[1]) };
}

/**
 * Recomputes the RFC 7638 thumbprint of the key in a credential file, inside
 * the page, using the same WebCrypto the wizard used.
 *
 * This is the check that matters in `handle.spec.ts`: it imports the PRIVATE
 * key from the downloaded PEM, derives the public half from it, canonicalises
 * exactly `{"crv","kty","x","y"}` in lexicographic order per RFC 7638, and
 * hashes that. If it equals the license's `cnf.jkt`, then the key in the file
 * really is the key catlog bound the license to — which is the one property that
 * makes the credential usable at all (§4.6).
 */
export async function thumbprintOfCredential(page: Page, pem: string): Promise<string> {
  return page.evaluate(async (pemText: string) => {
    const b64 = pemText.replace(/-----[^-]+-----/g, "").replace(/\s+/g, "");
    const der = Uint8Array.from(atob(b64), (c) => c.charCodeAt(0));
    const priv = await crypto.subtle.importKey(
      "pkcs8",
      der,
      { name: "ECDSA", namedCurve: "P-256" },
      true,
      ["sign"],
    );
    const jwk = await crypto.subtle.exportKey("jwk", priv);
    // RFC 7638: the required members only, lexicographically ordered, no space.
    const canonical = JSON.stringify({ crv: jwk.crv, kty: jwk.kty, x: jwk.x, y: jwk.y });
    const digest = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(canonical));
    let binary = "";
    for (const b of new Uint8Array(digest)) binary += String.fromCharCode(b);
    return btoa(binary).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
  }, pem);
}

/** POSTs to the loopback-only admin mux (§5.9). */
export async function admin(request: APIRequestContext, path: string, data: unknown = {}): Promise<any> {
  const res = await request.post(`${ADMIN_URL}${path}`, { data });
  const body = await res.text();
  if (!res.ok()) throw new Error(`POST ${path} → ${res.status()}: ${body}`);
  return body ? JSON.parse(body) : null;
}

/**
 * Pushes one §4.1 event through `POST /admin/events` and waits for it to be
 * folded. Returns once the feed row (if the type produces one) has been
 * published to every live SSE subscriber.
 */
export async function pushEvent(
  request: APIRequestContext,
  handle: string,
  type: string,
  payload: Record<string, unknown>,
  extra: Record<string, unknown> = {},
): Promise<any> {
  return admin(request, "/admin/events", {
    handle,
    events: [{ type, ver: 1, sim_t: 1, payload, ...extra }],
  });
}

/** A ULID good enough to be a flight or session id in a hand-built event. */
export function ulid(): string {
  const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ";
  let out = "";
  for (let i = 0; i < 26; i += 1) out += alphabet[Math.floor(Math.random() * alphabet.length)];
  // A ULID's first character encodes the top bits of the timestamp and must not
  // overflow 48 bits; anything above '7' does.
  return "0" + out.slice(1);
}
