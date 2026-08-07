/**
 * me.js — the handle this browser calls "me", and the theme switch.
 *
 * # Why this exists at all
 *
 * Every public page catlog serves carries `Cache-Control: public, s-maxage=30`,
 * so a shared cache is allowed to hand the same bytes to everybody. There is
 * therefore *no* server-rendered personalisation available on any public page —
 * not "we chose not to", but "the response is not yours to personalise". The
 * handle lives in `localStorage` under `catlog:me`, it is never sent to catlog as
 * an identifier, and everything it lights up is done here.
 *
 * The whole client-side budget for this site is the vendored datastar bundle,
 * keygen.js on the dashboard, the inline theme script in <head>, and this file.
 * Keep it that way: staying lean is this frontend's half of the bake-off.
 *
 * # What it does
 *
 *   - header chip, with a clear control
 *   - "This is me" / "This is not me" on a profile
 *   - highlights the reader's row on any board table on the page
 *   - a strip at the foot of a board when their row is on another page
 *   - the "Your standing" panel on the front page
 *   - the theme toggle (light / dark / follow the system)
 *
 * # Two rules it must not break
 *
 *   - **Never auto-clear.** A 404 for a stored handle can be a moderation action
 *     that gets reversed, a rebuild, or an incident. The stored value is the
 *     user's data; the notice offers to forget it and does not do so itself.
 *   - **Distinguish a 404 from a failure.** A network error, an offline browser
 *     or a refused request shows *nothing at all*. Only a real 404 says the
 *     profile is gone, and even then it repeats the API's own silence: catlog
 *     answers 404 identically for unknown, retired and banned, so the copy says
 *     "no public profile" and stops rather than guessing which.
 */

const ME_KEY = "catlog:me";
const THEME_KEY = "catlog:theme";
/** Kept in step with web.BoardRows — the row cap on /boards/{stat}. */
const BOARD_ROWS = 100;

const $ = (id) => document.getElementById(id);

/** Reads the stored handle. Storage can be denied outright; that is not an error. */
function storedMe() {
  try {
    return localStorage.getItem(ME_KEY) || "";
  } catch {
    return "";
  }
}

function setMe(handle) {
  try {
    if (handle) localStorage.setItem(ME_KEY, handle);
    else localStorage.removeItem(ME_KEY);
  } catch {
    /* A browser that refuses storage still gets a working site. */
  }
  if (handle) document.documentElement.setAttribute("data-me", handle);
  else document.documentElement.removeAttribute("data-me");
  render();
}

/** Case-insensitive, because a handle's display casing is not its identity. */
const sameHandle = (a, b) => a.toLowerCase() === b.toLowerCase();

// --- theme -------------------------------------------------------------------

/**
 * light → dark → system → light. `system` removes the attribute so the media
 * query in the stylesheet wins again; the <head> script applies whatever this
 * left behind, before first paint.
 */
function cycleTheme() {
  let current;
  try {
    current = localStorage.getItem(THEME_KEY) || "system";
  } catch {
    current = "system";
  }
  const next = current === "light" ? "dark" : current === "dark" ? "system" : "light";
  try {
    if (next === "system") localStorage.removeItem(THEME_KEY);
    else localStorage.setItem(THEME_KEY, next);
  } catch {
    /* ignore */
  }
  if (next === "system") document.documentElement.removeAttribute("data-theme");
  else document.documentElement.setAttribute("data-theme", next);
  const button = $("theme-toggle");
  if (button) button.title = `Theme: ${next}`;
}

// --- rendering ----------------------------------------------------------------

function renderChip(me) {
  const chip = $("me-chip");
  if (!chip) return;
  const link = $("me-link");
  if (!me) {
    chip.hidden = true;
    return;
  }
  chip.hidden = false;
  if (link) {
    link.textContent = me;
    link.href = `/p/${encodeURIComponent(me)}`;
  }
}

function renderProfileToggle(me) {
  const button = $("profile-me-toggle");
  if (!button) return;
  const handle = button.dataset.handle || "";
  const mine = me && sameHandle(me, handle);
  button.textContent = mine ? "This is not me" : "This is me";
  button.dataset.mine = mine ? "true" : "false";
  const note = $("profile-me-note");
  if (note) note.hidden = !mine;
}

/** Marks the reader's row wherever a board table is showing one. */
function renderRows(me) {
  for (const row of document.querySelectorAll("tr[data-handle]")) {
    const mine = !!me && sameHandle(me, row.dataset.handle || "");
    row.classList.toggle("is-me", mine);
  }
  for (const cell of document.querySelectorAll("#compare-table th[data-handle]")) {
    cell.classList.toggle("is-me", !!me && sameHandle(me, cell.dataset.handle || ""));
  }
}

/**
 * Fetches the stored handle's profile. Returns the document, `null` for a real
 * 404, and `undefined` for anything else — which is the distinction the whole
 * "never guess" rule rests on, so it is a three-way answer rather than a boolean.
 */
async function fetchProfile(handle) {
  try {
    const res = await fetch(`/v1/players/${encodeURIComponent(handle)}`, { headers: { accept: "application/json" } });
    if (res.status === 404) return null;
    if (!res.ok) return undefined;
    return await res.json();
  } catch {
    return undefined;
  }
}

/** The notice for a stored handle that no longer resolves. Keep it, or forget it. */
function renderGone(handle) {
  if ($("me-gone")) return;
  const main = $("main");
  if (!main) return;
  const box = document.createElement("aside");
  box.id = "me-gone";
  box.className = "panel";
  box.setAttribute("role", "status");
  const text = document.createElement("p");
  // The API answers 404 the same way for unknown, retired and banned. Saying
  // any more than this would be inventing a fact for the sake of a sentence.
  text.textContent = `catlog has no public profile for ${handle} any more.`;
  const actions = document.createElement("p");
  actions.className = "row";
  const keep = document.createElement("button");
  keep.type = "button";
  keep.className = "secondary";
  keep.textContent = "Keep it";
  keep.addEventListener("click", () => box.remove());
  const forget = document.createElement("button");
  forget.type = "button";
  forget.className = "secondary";
  forget.textContent = "Forget it";
  forget.addEventListener("click", () => {
    setMe("");
    box.remove();
  });
  actions.append(keep, forget);
  box.append(text, actions);
  main.prepend(box);
}

/** The front page's "Your standing": the three best ranks, and a way in. */
function renderStanding(profile) {
  const panel = $("me-standing");
  const rows = $("me-standing-rows");
  if (!panel || !rows) return;
  const link = $("me-standing-link");
  if (link) link.href = `/p/${encodeURIComponent(profile.handle)}`;

  rows.replaceChildren();
  const best = [...(profile.stats || [])].sort((a, b) => a.rank - b.rank).slice(0, 3);
  if (best.length === 0) {
    const empty = document.createElement("p");
    empty.className = "empty";
    empty.textContent = `${profile.handle} is not on any board yet. Fly something.`;
    rows.append(empty);
  }
  for (const stat of best) {
    const row = document.createElement("div");
    row.className = "standing-row";
    row.dataset.stat = stat.stat;
    const name = document.createElement("a");
    name.href = `/boards/${encodeURIComponent(stat.stat)}`;
    name.textContent = stat.title;
    const rank = document.createElement("span");
    rank.className = "rank";
    rank.textContent = `#${stat.rank} of ${stat.players}`;
    row.append(name, rank);
    rows.append(row);
  }
  panel.hidden = false;
}

/**
 * The strip at the foot of a board when the reader is on it but not on this
 * page. The offset is computed from the rank the profile already carries, which
 * is why "show me where I sit" needs no `?around=` endpoint.
 */
function renderBoardStrip(profile) {
  const strip = $("board-me");
  const title = $("board-title");
  if (!strip || !title) return;
  const stat = title.dataset.stat;
  const mine = (profile.stats || []).find((s) => s.stat === stat);
  if (!mine) return;
  if (document.querySelector("tr.is-me")) return; // already on this page

  const offset = Math.floor((mine.rank - 1) / BOARD_ROWS) * BOARD_ROWS;
  const text = $("board-me-text");
  if (text) text.textContent = `You: #${mine.rank} of ${mine.players}`;
  const link = $("board-me-link");
  if (link) {
    const url = new URL(window.location.href);
    if (offset > 0) url.searchParams.set("offset", String(offset));
    else url.searchParams.delete("offset");
    link.href = `${url.pathname}${url.search}`;
  }
  strip.hidden = false;
}

function render() {
  const me = storedMe();
  renderChip(me);
  renderProfileToggle(me);
  renderRows(me);
}

/** Everything that needs the profile document, in one request. */
async function personalise() {
  const me = storedMe();
  if (!me) return;
  const needsProfile = $("me-standing") || $("board-me");
  if (!needsProfile) return;

  const profile = await fetchProfile(me);
  if (profile === undefined) return; // offline, or the server is unwell: say nothing
  if (profile === null) {
    renderGone(me);
    return;
  }
  renderStanding(profile);
  renderBoardStrip(profile);
}

// --- wiring --------------------------------------------------------------------

$("me-clear")?.addEventListener("click", () => setMe(""));

$("theme-toggle")?.addEventListener("click", cycleTheme);

$("profile-me-toggle")?.addEventListener("click", (event) => {
  const button = event.currentTarget;
  setMe(button.dataset.mine === "true" ? "" : button.dataset.handle || "");
});

// The wizard's step 4 is the one moment this site knows the handle for certain.
$("wizard-set-me")?.addEventListener("click", () => {
  const claimed = $("wizard-claimed");
  if (claimed?.textContent) setMe(claimed.textContent.trim());
});

render();
personalise();
