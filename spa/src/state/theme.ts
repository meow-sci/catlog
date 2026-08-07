import { atom, onMount, type ReadableAtom } from 'nanostores';

/**
 * Light and dark, and neither is "the real one with the other bolted on".
 *
 * Resolution order (§2.1): `localStorage['catlog:theme']` ∈ {light, dark,
 * system} → `prefers-color-scheme` → dark. An explicit choice stamps
 * `data-theme` on `<html>`; `system` removes the attribute so the media query in
 * `index.css` wins again.
 *
 * **The first application happens in `index.html`, synchronously, before first
 * paint.** This module only handles changes afterwards. Anything asynchronous
 * here would produce a white flash on every dark-theme reload, which is the one
 * bug a theme toggle is judged on.
 */
export type ThemeChoice = 'light' | 'dark' | 'system';

export const THEME_KEY = 'catlog:theme';

function isChoice(value: string | null): value is ThemeChoice {
  return value === 'light' || value === 'dark' || value === 'system';
}

function readChoice(): ThemeChoice {
  try {
    const raw = window.localStorage.getItem(THEME_KEY);
    return isChoice(raw) ? raw : 'system';
  } catch {
    return 'system';
  }
}

const store = atom<ThemeChoice>(typeof window === 'undefined' ? 'system' : readChoice());

onMount(store, () => {
  store.set(readChoice());
  const sync = (event: StorageEvent) => {
    if (event.key === null || event.key === THEME_KEY) store.set(readChoice());
  };
  window.addEventListener('storage', sync);
  return () => {
    window.removeEventListener('storage', sync);
  };
});

/** The viewer's explicit choice, or `system`. */
export const $theme: ReadableAtom<ThemeChoice> = store;

/**
 * Applies a choice: persists it, updates the store, and stamps the attribute.
 *
 * The DOM write lives here rather than in an effect because it is the same write
 * `index.html` does before paint, and having exactly one function that knows the
 * rule is what keeps the two from drifting.
 */
export function setTheme(choice: ThemeChoice): void {
  try {
    window.localStorage.setItem(THEME_KEY, choice);
  } catch {
    // A browser that refuses storage still gets the theme for this page.
  }
  applyTheme(choice);
  store.set(choice);
}

/** Stamps (or removes) `data-theme` on `<html>`. */
export function applyTheme(choice: ThemeChoice): void {
  if (choice === 'system') {
    document.documentElement.removeAttribute('data-theme');
  } else {
    document.documentElement.setAttribute('data-theme', choice);
  }
}

/** What the page actually looks like right now, given a choice and the OS. */
export function resolveTheme(choice: ThemeChoice): 'light' | 'dark' {
  if (choice !== 'system') return choice;
  return window.matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark';
}
