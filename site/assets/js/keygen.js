/**
 * catlog dashboard: the four-step credential wizard and the account actions
 * (INITIAL_IMPL_PLAN §5.7). A plain ES module — no framework, no build-time
 * templating, no HTML built from strings.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 * THE ONE INVARIANT: the private key never leaves this page.
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * `crypto.subtle.generateKey` hands back two *separate* key objects. Everything
 * below is arranged so the private one only ever reaches a Blob:
 *
 *   1. `publicJwk()` exports `keyPair.publicKey` — never the pair, never the
 *      private half. A JWK exported from a private ECDSA key contains `d`, the
 *      scalar that *is* the key (§13 risk 6), so exporting the wrong object is
 *      the single catastrophic mistake available here.
 *   2. It then builds a brand-new object with exactly the four §4.5.2 members
 *      `{kty, crv, x, y}`. It is a whitelist, not a delete: a future WebCrypto
 *      that adds a field cannot leak it by default.
 *   3. `assertPublicOnly()` re-checks the object it is about to serialise and
 *      throws rather than send anything unexpected. Defence in depth — the
 *      server rejects a `d`-bearing JWK with `bad_request` as well — but the
 *      check that matters is the one on this side, because it is the only one
 *      that runs before the bytes exist on a network.
 *   4. `postJSON()` is the only function in this file that calls `fetch`, and
 *      the private key is not in scope inside it.
 *
 * The private half is exported exactly once, as PKCS#8, straight into the
 * credential JSON that becomes a Blob and a download. That is why catlog cannot
 * re-issue the same file: it never had the key.
 */

/** §4.6 credential file. */
const CREDENTIAL_FILENAME = "catlog-credential.json";
const CREDENTIAL_FORMAT = 1;

/**
 * §4.7's handle regex, mirrored here only so an obvious typo is caught without a
 * round trip. The server is the authority and re-checks everything.
 */
const HANDLE_RE = /^[A-Za-z0-9]([A-Za-z0-9._-]{0,148}[A-Za-z0-9])?$/;

/** The four members a public EC JWK may have on the wire (§4.5.2). */
const PUBLIC_JWK_MEMBERS = ["kty", "crv", "x", "y"];

/**
 * Human text for the §4.9 codes the wizard can provoke. Unknown codes fall back
 * to the server's `detail`, so a new code degrades to "less friendly" rather
 * than "silent".
 */
const ERROR_TEXT = {
  handle_taken: "That handle is already taken. Handles are never recycled, so this one is gone for good.",
  handle_invalid: "That is not a valid handle: 1–150 characters, letters, digits, dot, underscore or hyphen, starting and ending with a letter or digit.",
  handle_reserved: "That handle is reserved.",
  quota_exceeded: "You have hit a quota — either the number of handles you may hold or the number of licenses you may issue in 24 hours.",
  account_too_new: "Your identity-provider account is too new. catlog requires an account at least 30 days old.",
  banned: "This account has been banned or deleted.",
  not_found: "You do not hold that handle.",
  bad_request: "catlog rejected the request.",
  internal: "catlog hit an internal error. Nothing was changed.",
};

// --- tiny DOM helpers ---------------------------------------------------------

const $ = (id) => document.getElementById(id);
const show = (el) => el && el.removeAttribute("hidden");
const hide = (el) => el && el.setAttribute("hidden", "");

/** Switches the wizard to one of its four steps (§5.7). */
function step(n) {
  for (let i = 1; i <= 4; i += 1) {
    const el = $(`wizard-step-${i}`);
    if (!el) continue;
    if (i === n) show(el);
    else hide(el);
  }
}

/**
 * Renders an error into a `[data-error]` container. The code goes in the
 * attribute as well as the text so a test — and a bug report — can name it.
 */
function showError(container, code, detail) {
  if (!container) return;
  container.dataset.error = code || "error";
  const codeEl = container.querySelector("strong") || container;
  const detailEl = container.querySelector("span");
  const text = ERROR_TEXT[code] || detail || "Something went wrong.";
  if (detailEl) {
    codeEl.textContent = code || "";
    detailEl.textContent = text;
  } else {
    container.textContent = `${code || ""} ${text}`.trim();
  }
  show(container);
}

function clearError(container) {
  if (!container) return;
  container.dataset.error = "";
  hide(container);
}

/**
 * Two-click confirmation for a destructive button.
 *
 * `window.confirm` is deliberately not used: it is a browser-chrome dialog, so
 * it is invisible to the page, awkward to test, and gets auto-dismissed by
 * automation. Arming the button leaves the state in the DOM where everything
 * can see it.
 */
function arm(button, armedLabel) {
  if (button.dataset.armed === "true") return true;
  const original = button.textContent;
  button.dataset.armed = "true";
  button.textContent = armedLabel;
  const disarm = () => {
    button.dataset.armed = "false";
    button.textContent = original;
  };
  button.addEventListener("blur", disarm, { once: true });
  return false;
}

// --- HTTP ----------------------------------------------------------------------

/**
 * The only network call in this module. Same-origin, so
 * `Sec-Fetch-Site: same-origin` satisfies Go's CrossOriginProtection with no
 * token plumbing (§4.5.4), and the session cookie rides along.
 *
 * @returns {Promise<{ok: boolean, status: number, data: any}>}
 */
async function postJSON(url, body) {
  const res = await fetch(url, {
    method: "POST",
    credentials: "same-origin",
    headers: { "Content-Type": "application/json", Accept: "application/json" },
    body: JSON.stringify(body ?? {}),
  });
  let data = null;
  try {
    data = await res.json();
  } catch {
    data = null;
  }
  return { ok: res.ok, status: res.status, data };
}

// --- keys -----------------------------------------------------------------------

/** Generates the ES256 key pair the license will be bound to (§4.5). */
function generateKeyPair() {
  return crypto.subtle.generateKey({ name: "ECDSA", namedCurve: "P-256" }, true, ["sign"]);
}

/**
 * Exports the PUBLIC half as the §4.5.2 `{kty, crv, x, y}` JWK.
 *
 * Note the argument: `keyPair.publicKey`, never `keyPair` and never
 * `keyPair.privateKey`. See the module header.
 */
async function publicJwk(publicKey) {
  const exported = await crypto.subtle.exportKey("jwk", publicKey);
  const jwk = {};
  for (const member of PUBLIC_JWK_MEMBERS) jwk[member] = exported[member];
  assertPublicOnly(jwk);
  return jwk;
}

/**
 * Throws unless `jwk` is exactly a P-256 public key. Called immediately before
 * the value is serialised, so it sees what would actually be sent.
 */
function assertPublicOnly(jwk) {
  for (const key of Object.keys(jwk)) {
    if (!PUBLIC_JWK_MEMBERS.includes(key)) {
      throw new Error(`refusing to send a JWK member: ${key}`);
    }
  }
  if ("d" in jwk) throw new Error("refusing to send a private key");
  if (jwk.kty !== "EC" || jwk.crv !== "P-256") {
    throw new Error(`expected an EC P-256 key, got ${jwk.kty}/${jwk.crv}`);
  }
  if (!jwk.x || !jwk.y) throw new Error("the exported public key is incomplete");
}

/** Exports the private half as a PKCS#8 PEM — straight into the download. */
async function privateKeyPem(privateKey) {
  const pkcs8 = await crypto.subtle.exportKey("pkcs8", privateKey);
  return pemWrap("PRIVATE KEY", new Uint8Array(pkcs8));
}

function pemWrap(label, bytes) {
  let binary = "";
  for (const b of bytes) binary += String.fromCharCode(b);
  const body = btoa(binary).replace(/(.{64})/g, "$1\n").trimEnd();
  return `-----BEGIN ${label}-----\n${body}\n-----END ${label}-----\n`;
}

// --- the credential file ----------------------------------------------------------

/** Assembles §4.6's file. Assembled here, never on the server. */
function credentialFile(handle, license, pem) {
  return JSON.stringify({ format: CREDENTIAL_FORMAT, handle, license, private_key_pem: pem }, null, 2) + "\n";
}

/** Object URLs stay alive for the page so the "download again" link works. */
const liveObjectURLs = [];
addEventListener("pagehide", () => {
  for (const url of liveObjectURLs) URL.revokeObjectURL(url);
  liveObjectURLs.length = 0;
});

/** Offers `json` as a download and returns the URL the anchor can reuse. */
function offerDownload(json) {
  const url = URL.createObjectURL(new Blob([json], { type: "application/json" }));
  liveObjectURLs.push(url);

  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = CREDENTIAL_FILENAME;
  anchor.rel = "noopener";
  anchor.style.display = "none";
  document.body.appendChild(anchor);
  anchor.click();
  anchor.remove();
  return url;
}

// --- the wizard --------------------------------------------------------------------

/**
 * Runs steps 2–4 for both a new claim and a reissue: they differ only in the
 * endpoint and the body, and the part that must not be got wrong — which half
 * of the key pair is sent — is identical.
 */
async function issue({ url, handle, body, errorBox, onDone }) {
  clearError(errorBox);

  // Step 2: generate. Nothing has been sent yet.
  step(2);
  let keyPair;
  try {
    keyPair = await generateKeyPair();
  } catch (err) {
    step(1);
    showError(errorBox, "internal", `This browser could not generate a P-256 key: ${err.message}`);
    return;
  }

  let jwk;
  try {
    jwk = await publicJwk(keyPair.publicKey);
  } catch (err) {
    step(1);
    showError(errorBox, "internal", err.message);
    return;
  }

  // Step 3: claim. Only `jwk` — the public half — crosses the network.
  step(3);
  const res = await postJSON(url, { ...body, jwk });
  if (!res.ok || !res.data || !res.data.license) {
    step(1);
    const code = res.data?.error || "internal";
    showError(errorBox, code, res.data?.detail);
    return;
  }

  // Step 4: assemble the credential and hand it over. This is the only moment
  // the private key is serialised, and it goes to a Blob.
  const pem = await privateKeyPem(keyPair.privateKey);
  const json = credentialFile(res.data.handle || handle, res.data.license, pem);
  const url2 = offerDownload(json);

  const download = $("wizard-download");
  if (download) download.href = url2;
  const claimed = $("wizard-claimed");
  if (claimed) claimed.textContent = res.data.handle || handle;
  const jkt = $("wizard-jkt");
  if (jkt) jkt.textContent = res.data.jkt || "";
  const expires = $("wizard-expires");
  if (expires && res.data.expires_at) {
    expires.textContent = new Date(res.data.expires_at).toISOString().replace("T", " ").slice(0, 16) + " UTC";
  }
  step(4);
  if (onDone) onDone(res.data);
}

function wireWizard() {
  const wizard = $("wizard");
  if (!wizard) return;
  const errorBox = $("wizard-error");
  const input = $("wizard-handle");
  const submit = $("wizard-submit");
  if (!submit || !input) return;

  const claim = async () => {
    const handle = input.value.trim();
    if (!HANDLE_RE.test(handle)) {
      showError(errorBox, "handle_invalid", null);
      return;
    }
    submit.setAttribute("aria-busy", "true");
    submit.disabled = true;
    try {
      await issue({ url: "/api/handles", handle, body: { handle }, errorBox });
    } finally {
      submit.removeAttribute("aria-busy");
      submit.disabled = false;
    }
  };

  submit.addEventListener("click", claim);
  input.addEventListener("keydown", (e) => {
    if (e.key === "Enter") {
      e.preventDefault();
      claim();
    }
  });

  const done = $("wizard-done");
  // Reloading is deliberate: the handle list is server-rendered, and re-fetching
  // the page is one source of truth rather than two renderers of the same data.
  if (done) done.addEventListener("click", () => window.location.reload());
}

// --- per-handle actions ---------------------------------------------------------------

function wireHandleActions() {
  for (const button of document.querySelectorAll(".catlog-reissue")) {
    button.addEventListener("click", async () => {
      const handle = button.dataset.handle;
      const errorBox = button.closest("footer")?.querySelector(".handle-error");
      if (!arm(button, "Reissue and revoke the old key?")) return;
      button.disabled = true;
      button.setAttribute("aria-busy", "true");
      try {
        await issue({
          url: `/api/handles/${encodeURIComponent(handle)}/reissue`,
          handle,
          body: {},
          errorBox: $("wizard-error") || errorBox,
        });
        document.getElementById("wizard")?.scrollIntoView({ block: "start" });
      } finally {
        button.disabled = false;
        button.removeAttribute("aria-busy");
      }
    });
  }

  for (const button of document.querySelectorAll(".catlog-revoke")) {
    button.addEventListener("click", async () => {
      const handle = button.dataset.handle;
      const errorBox = button.closest("footer")?.querySelector(".handle-error");
      clearError(errorBox);
      if (!arm(button, "Click again to revoke")) return;

      button.disabled = true;
      const res = await postJSON(`/api/handles/${encodeURIComponent(handle)}/revoke`, {});
      button.disabled = false;
      if (!res.ok) {
        showError(errorBox, res.data?.error || "internal", res.data?.detail);
        return;
      }
      window.location.reload();
    });
  }
}

// --- account actions -------------------------------------------------------------------

function wireAccountActions() {
  const logout = $("logout");
  if (logout) {
    logout.addEventListener("click", async () => {
      await postJSON("/api/logout", {});
      window.location.assign("/");
    });
  }

  const del = $("delete-account");
  if (del) {
    const errorBox = $("delete-error");
    del.addEventListener("click", async () => {
      clearError(errorBox);
      if (!arm(del, "Click again to delete everything")) return;

      del.disabled = true;
      const res = await postJSON("/api/me/delete", {});
      del.disabled = false;
      if (!res.ok) {
        showError(errorBox, res.data?.error || "internal", res.data?.detail);
        return;
      }
      window.location.assign("/");
    });
  }
}

// --- boot -------------------------------------------------------------------------------

function boot() {
  if (!globalThis.crypto?.subtle) {
    // WebCrypto needs a secure context: https, or http on localhost/127.0.0.1.
    const errorBox = $("wizard-error");
    showError(errorBox, "internal",
      "This browser has no WebCrypto here. catlog needs a secure context (https, or localhost) to generate your key.");
    return;
  }
  wireWizard();
  wireHandleActions();
  wireAccountActions();
}

if (document.readyState === "loading") {
  document.addEventListener("DOMContentLoaded", boot, { once: true });
} else {
  boot();
}

export { assertPublicOnly, credentialFile, pemWrap, publicJwk, HANDLE_RE };
