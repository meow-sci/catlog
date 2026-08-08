/**
 * intl.js — the numbers this server rendered, re-rendered in the reader's own.
 *
 * # Why the server cannot just do this
 *
 * Every public page carries `Cache-Control: public, s-maxage=30` (§4.8), so a
 * shared cache is allowed to hand one response to everybody. There is therefore
 * no locale available at render time — not "we chose not to look", but "the
 * response is not yours to vary". It is exactly the argument me.js makes about
 * the stored handle, applied to a thousands separator.
 *
 * The alternative would be `Vary: Accept-Language` on every public page, which
 * is a high-cardinality header and would shred the CDN cache the whole §4.8
 * design exists to fill. So the server renders `units.Format`'s canonical
 * en-US grouping and this finishes the job on arrival.
 *
 * # Why it re-renders rather than swapping a character
 *
 * Because grouping is not one character. `en-IN` writes 12,34,567 — groups of
 * two after the first three. `es-ES` leaves 1234 alone and groups 12345.
 * `fr-FR`'s separator is U+202F, which is the joke: the narrow no-break space
 * this site used to show *everybody* is one locale's answer, not a neutral one.
 * Nothing short of `Intl.NumberFormat` over the actual number gets those right,
 * which is why `units.Split` publishes the number and the precision as
 * attributes instead of leaving this to parse the text back.
 *
 * # The contract with the server
 *
 *   <span class="n" data-n="1.82" data-d="2">1.82</span> Mm
 *
 * `data-n` is the **scaled** number the reader sees — 1 820 000 m has already
 * become 1.82 Mm server-side, because which SI prefix to use is a `units` rule
 * and not a locale's business. `data-d` is how many decimals it is shown to,
 * after rule 2's trailing-zero trim. The element's text is the canonical
 * rendering, which is what a reader with JavaScript off keeps, and what the
 * first frame shows before this runs.
 *
 * Anything `units` cannot express as a single number — "243d 01h", "1h 01m",
 * "—" — is emitted with no element at all and is never touched here.
 */

/** The element the server marks a re-renderable number with. */
const SELECTOR = "span.n[data-n]";

/**
 * Stamped on an element once it has been localised.
 *
 * Load-bearing, not bookkeeping: this writes to `textContent`, the observer
 * below watches for exactly that, and without a mark the two would chase each
 * other. It is also what makes a second pass over a page free.
 */
const DONE = "data-localized";

/** `Intl.NumberFormat` per precision. Building one is the expensive part. */
const formatters = new Map();

function formatter(decimals) {
  let f = formatters.get(decimals);
  if (!f) {
    // No locale argument: `undefined` is Intl for "this browser's", which is
    // the entire point of the file. `useGrouping` is left at its default of
    // `auto`, so a locale that does not group four digits does not get them
    // grouped. `minimumFractionDigits: 0` preserves rule 2's trailing-zero
    // trim, which the server already applied and `data-d` already reflects.
    f = new Intl.NumberFormat(undefined, {
      minimumFractionDigits: 0,
      maximumFractionDigits: decimals,
    });
    formatters.set(decimals, f);
  }
  return f;
}

/** Re-renders one marked number. A malformed attribute leaves the text alone. */
function localize(el) {
  el.setAttribute(DONE, "");
  const n = Number(el.getAttribute("data-n"));
  if (!Number.isFinite(n)) return;
  // Clamped to what every engine accepts for maximumFractionDigits; `units`
  // never asks for more than 6, so this only bounds a corrupted attribute.
  const d = Math.min(Math.max(Number(el.getAttribute("data-d")) || 0, 0), 20);
  const text = formatter(d).format(n);
  if (el.textContent !== text) el.textContent = text;
}

/** Re-renders every number under `root` that has not been done already. */
function localizeAll(root) {
  if (root.nodeType !== Node.ELEMENT_NODE) return;
  if (root.matches(SELECTOR) && !root.hasAttribute(DONE)) localize(root);
  for (const el of root.querySelectorAll(`${SELECTOR}:not([${DONE}])`)) localize(el);
}

/**
 * Starts localising, and keeps doing it.
 *
 * A MutationObserver rather than a one-shot pass, because half the numbers on
 * this site arrive after the document does: datastar patches the feed list, the
 * live event rows and the search suggestions in over SSE, and every one of
 * those carries value cells. Hooking datastar's own events instead would tie
 * this to a vendored bundle's API for no benefit — new nodes are new nodes.
 */
function start() {
  localizeAll(document.body);
  new MutationObserver((records) => {
    for (const record of records) {
      for (const node of record.addedNodes) localizeAll(node);
    }
  }).observe(document.body, { childList: true, subtree: true });
}

if (document.readyState === "loading") {
  document.addEventListener("DOMContentLoaded", start, { once: true });
} else {
  start();
}
