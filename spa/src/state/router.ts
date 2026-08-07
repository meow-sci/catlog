import { atom, onMount, type ReadableAtom } from 'nanostores';

/**
 * Hash-based client routing.
 *
 * # Why the hash, and not `404.html`
 *
 * GitHub Pages has no rewrite rules. The two ways to deep-link a SPA there are
 * a `404.html` copy of `index.html` (Pages serves it for any unmatched path,
 * the app reads `location.pathname` and re-renders) or the fragment. This app
 * uses the fragment, on purpose:
 *
 *   - the `404.html` trick answers every deep link with HTTP **404**. Browsers
 *     render it, but caches, uptime checks and anything reading status codes
 *     see a broken site, and a CDN that intercepts 404s breaks it outright.
 *   - the fragment is never sent to the server, so `/catlog/#/boards/rud_total`
 *     is one 200 for `index.html` from any host, at any base path, with no
 *     repository settings to enable and nothing to keep in sync with `base`.
 *   - the cost is SEO and pretty URLs — and the server-rendered site at
 *     `site/` already owns both. This frontend is the second view of the same
 *     data, not the canonical one.
 *
 * That trade is the honest one *because* there are two frontends. A lone SPA
 * with no SSR sibling should take the `404.html` route and the real URLs.
 */

export type Route =
  | { readonly name: 'home' }
  | { readonly name: 'boards' }
  | { readonly name: 'board'; readonly stat: string; readonly offset: number }
  | { readonly name: 'player'; readonly handle: string }
  | { readonly name: 'notFound'; readonly path: string };

/** How many rows a board page shows. The server clamps anything over 200 (§4.8). */
export const PAGE_SIZE = 50;

/**
 * Parses a `location.hash` into a route.
 *
 * Pure and total: an unrecognised path is a `notFound` route rather than a throw,
 * because the input is a user-editable address bar.
 */
export function parseRoute(hash: string): Route {
  const raw = hash.startsWith('#') ? hash.slice(1) : hash;
  const [path = '', query = ''] = raw.split('?', 2);
  const segments = path.split('/').filter((s) => s !== '');

  if (segments.length === 0) return { name: 'home' };

  const [head, tail] = segments;
  if (head === 'boards' && segments.length === 1) return { name: 'boards' };
  if (head === 'boards' && segments.length === 2 && tail !== undefined) {
    const offset = Number.parseInt(new URLSearchParams(query).get('offset') ?? '', 10);
    return {
      name: 'board',
      stat: decodeURIComponent(tail),
      offset: Number.isFinite(offset) && offset > 0 ? offset : 0,
    };
  }
  if (head === 'p' && segments.length === 2 && tail !== undefined) {
    return { name: 'player', handle: decodeURIComponent(tail) };
  }
  return { name: 'notFound', path };
}

/** The hash for a route — the single place link targets are constructed. */
export function hrefFor(route: Route): string {
  switch (route.name) {
    case 'home':
      return '#/';
    case 'boards':
      return '#/boards';
    case 'board':
      return route.offset > 0
        ? `#/boards/${encodeURIComponent(route.stat)}?offset=${String(route.offset)}`
        : `#/boards/${encodeURIComponent(route.stat)}`;
    case 'player':
      return `#/p/${encodeURIComponent(route.handle)}`;
    case 'notFound':
      return `#${route.path}`;
  }
}

/**
 * A stable key for a route: what a data hook keys its fetch on.
 *
 * Separate from `hrefFor` because it must not change when the *display* of a URL
 * changes — and because two routes that render the same data must produce the
 * same key.
 */
export function routeKey(route: Route): string {
  switch (route.name) {
    case 'board':
      return `board:${route.stat}:${String(route.offset)}`;
    case 'player':
      return `player:${route.handle}`;
    default:
      return route.name;
  }
}

const currentHash = (): string => (typeof window === 'undefined' ? '' : window.location.hash);

/**
 * The current route.
 *
 * A lazy store (nanostores' mount/disabled modes): the `hashchange` listener
 * only exists while something is rendering it, and is removed a second after the
 * last subscriber goes away. Nothing in a component has to remember to unbind.
 */
export const $route: ReadableAtom<Route> = (() => {
  const store = atom<Route>(parseRoute(currentHash()));
  onMount(store, () => {
    const sync = () => {
      store.set(parseRoute(currentHash()));
    };
    sync();
    window.addEventListener('hashchange', sync);
    return () => {
      window.removeEventListener('hashchange', sync);
    };
  });
  return store;
})();

/**
 * Navigates by setting the hash.
 *
 * Assigning `location.hash` — rather than pushing history entries by hand — is
 * what keeps the back button, middle-click-to-open-in-a-new-tab and copying a
 * link out of the address bar all working without any code.
 */
export function navigate(route: Route): void {
  window.location.hash = hrefFor(route);
}
