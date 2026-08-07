import { atom, onMount, type ReadableAtom } from 'nanostores';

/**
 * The "me" handle — a local browser preference, not a login.
 *
 * # Why it lives in the browser
 *
 * That is forced rather than chosen. Every public read response is served
 * `Cache-Control: public, s-maxage=30` to a shared cache, so **nothing
 * personalised can ever be server-rendered on a public page**. Both of catlog's
 * frontends therefore do the same thing in the same place: the datastar site
 * stamps `data-me` on `<html>` from a tiny script and lets CSS do the rest; this
 * one keeps a nanostores atom over the same key.
 *
 * # What it is not
 *
 * It is never sent to catlog as an identifier, never appears in a query string
 * on a cached URL, never in a `Referer`, and it is not a session — the read API
 * takes no credentials at all and must not start. Two people on one machine
 * share it, exactly like a bookmark.
 *
 * # Storage
 *
 * Key `catlog:me`, value the handle as a plain string in display casing. One
 * key, no JSON envelope, so a curious user can read it and clear it. (Storage
 * does not cross origins, so the two frontends do not actually share a value;
 * the same key name means a future same-origin deployment would.)
 */
export const ME_KEY = 'catlog:me';

/**
 * Reads the stored handle.
 *
 * Total: a browser with storage disabled, a Safari private window, or an iframe
 * under a strict partitioning policy all throw from `localStorage`, and none of
 * them is a reason for the page not to render.
 */
function readMe(): string | null {
  try {
    const raw = window.localStorage.getItem(ME_KEY);
    return raw === null || raw.trim() === '' ? null : raw.trim();
  } catch {
    return null;
  }
}

const store = atom<string | null>(typeof window === 'undefined' ? null : readMe());

/**
 * A lazy store, so the `storage` listener only exists while something renders
 * it. `storage` fires in *other* tabs, which is what keeps a second tab from
 * showing a stale "You:" chip after this one changed it.
 */
onMount(store, () => {
  store.set(readMe());
  const sync = (event: StorageEvent) => {
    if (event.key === null || event.key === ME_KEY) store.set(readMe());
  };
  window.addEventListener('storage', sync);
  return () => {
    window.removeEventListener('storage', sync);
  };
});

/** The handle the viewer has claimed as theirs, or null. */
export const $me: ReadableAtom<string | null> = store;

/**
 * Handles whose "we could not find this any more" notice has been dismissed for
 * this page's lifetime.
 *
 * §7.1: when the stored handle stops resolving the UI **never auto-clears it**.
 * A 404 during an incident, a rebuild, or a moderation action that gets reversed
 * must not silently erase the user's own data. So the notice offers *Keep it*
 * — which lands here — and *Forget it*, which calls [clearMe].
 *
 * Deliberately not persisted: "dismissed for this session" means this page. It
 * is also cleared by [setMe] and [clearMe] — a notice about a handle you no
 * longer hold is not a notice worth remembering.
 */
const dismissed = atom<readonly string[]>([]);
export const $meDismissed: ReadableAtom<readonly string[]> = dismissed;

export function dismissMeNotice(handle: string): void {
  const lower = handle.toLowerCase();
  if (!dismissed.get().includes(lower)) dismissed.set([...dismissed.get(), lower]);
}

/** Claims a handle as "me". Called from an event handler, never from render. */
export function setMe(handle: string): void {
  dismissed.set([]);
  try {
    window.localStorage.setItem(ME_KEY, handle);
  } catch {
    // Storage refused. The atom still updates, so the session behaves; it just
    // will not survive a reload, which is better than the button doing nothing.
  }
  store.set(handle);
}

/** Forgets the handle. The user's data, cleared only when the user says so. */
export function clearMe(): void {
  dismissed.set([]);
  try {
    window.localStorage.removeItem(ME_KEY);
  } catch {
    // As above.
  }
  store.set(null);
}

/** Whether `handle` is the viewer's own, compared the way catlog compares handles. */
export function isMe(handle: string, me: string | null): boolean {
  return me !== null && handle.toLowerCase() === me.toLowerCase();
}
