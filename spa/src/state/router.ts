import { atom, onMount, type ReadableAtom } from 'nanostores';

/**
 * HTML5 History routing over real paths.
 *
 * `/`, `/boards`, `/boards/:stat`, `/p/:handle` — the URLs a user can type,
 * bookmark, paste into a chat window and send to somebody. Navigation is
 * `history.pushState` plus a store update; back and forward arrive as
 * `popstate`. There is no fragment anywhere in this file, on purpose: this SPA
 * is deployed to its own host on its own domain, possibly without the
 * server-rendered site running at all, so it has to own real URLs rather than
 * borrow them from a sibling.
 *
 * # What this costs, and who pays it
 *
 * Real paths mean the *host* has to answer a deep link with `index.html`
 * instead of a 404. Static hosts all support that, but each one wants it
 * spelled differently:
 *
 *   - GitHub Pages: a `404.html` that is a byte copy of `index.html`. The build
 *     emits it (`vite.config.ts`), so it cannot drift.
 *   - nginx: `try_files $uri $uri/ /index.html;`
 *   - Netlify / Cloudflare Pages: `/* /index.html 200` in `_redirects`.
 *   - Vite's own dev and preview servers: free, via `appType: 'spa'`.
 *
 * See `spa/README.md` for the same list with the surrounding detail.
 *
 * # Base path
 *
 * Nothing here hardcodes `/`. The deployment's base comes from Vite's `base`
 * (`import.meta.env.BASE_URL`), it is stripped when a location is read and
 * prepended when a link is written, so the same code serves `https://catlog.example/`
 * and `https://owner.github.io/catlog/` with no source change. Every exported
 * function takes the base as an optional last argument so a test can pin it.
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
 * Normalises anything Vite will accept as `base` into `/…/` form.
 *
 * Vite allows `''`, `'./'`, `'/catlog'`, `'/catlog/'` and even an absolute URL.
 * Every one of those has to end up as a path with exactly one leading and one
 * trailing slash, because the rest of this module joins and slices on that shape.
 */
export function normalizeBase(base: string): string {
  let path = base;
  // An absolute `base` (a CDN origin) still routes on its path component.
  if (path.includes('://')) path = new URL(path).pathname;
  if (path.startsWith('.')) path = path.slice(1);
  if (!path.startsWith('/')) path = '/' + path;
  if (!path.endsWith('/')) path += '/';
  return path;
}

/** The path this bundle is deployed under. `/` unless `SPA_BASE` said otherwise. */
export const BASE_PATH: string = normalizeBase(import.meta.env.BASE_URL);

/** Whether a pathname belongs to this app rather than to something else on the same origin. */
export function isUnderBase(pathname: string, base: string = BASE_PATH): boolean {
  if (base === '/') return true;
  // `/catlog` (no trailing slash) is the app's home too — that is the URL a user
  // types, and the one a host redirects from.
  return pathname === base.slice(0, -1) || pathname.startsWith(base);
}

/**
 * Removes the deployment base from a pathname, leaving the app-relative path.
 *
 * A pathname that is not under the base is returned untouched, so it falls
 * through to `notFound` rather than being silently truncated into a real route.
 */
export function stripBase(pathname: string, base: string = BASE_PATH): string {
  if (pathname === '') return '/';
  if (base === '/') return pathname;
  if (!isUnderBase(pathname, base)) return pathname;
  // `base.slice(0, -1)` drops the trailing slash so the result keeps its leading one.
  const stripped = pathname.slice(base.length - 1);
  return stripped === '' ? '/' : stripped;
}

/**
 * Parses a URL — `pathname` plus optional `?search` — into a route.
 *
 * Pure and total: an unrecognised path is a `notFound` route rather than a
 * throw, because the input is a user-editable address bar.
 */
export function parseRoute(url: string, base: string = BASE_PATH): Route {
  // A fragment is never part of a route here; drop it before anything else so a
  // pasted `…/boards/rud_total#top` still resolves to the board.
  const [withoutHash = ''] = url.split('#', 2);
  const [pathname = '', query = ''] = withoutHash.split('?', 2);
  // A path outside this deployment's base is not this app's to interpret.
  // Matching it anyway would turn a neighbour's `/boards/rud_total` into our
  // board route, which is a fabrication rather than a routing decision.
  if (pathname !== '' && !isUnderBase(pathname, base)) {
    return { name: 'notFound', path: pathname };
  }
  const path = stripBase(pathname, base);
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

/** The app-relative path for a route, with no deployment base on it. */
export function pathFor(route: Route): string {
  switch (route.name) {
    case 'home':
      return '/';
    case 'boards':
      return '/boards';
    case 'board':
      return route.offset > 0
        ? `/boards/${encodeURIComponent(route.stat)}?offset=${String(route.offset)}`
        : `/boards/${encodeURIComponent(route.stat)}`;
    case 'player':
      return `/p/${encodeURIComponent(route.handle)}`;
    case 'notFound':
      return route.path;
  }
}

/**
 * The `href` for a route — the single place link targets are constructed.
 *
 * Base-prefixed, so every `<a href>` in the app is a URL the host can serve
 * directly. A hardcoded `/` here is exactly how a subpath deployment breaks
 * silently, which is why the base is threaded through rather than assumed.
 */
export function hrefFor(route: Route, base: string = BASE_PATH): string {
  const path = pathFor(route);
  return base + (path.startsWith('/') ? path.slice(1) : path);
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

const currentUrl = (): string =>
  typeof window === 'undefined' ? '/' : window.location.pathname + window.location.search;

/**
 * The current route.
 *
 * A lazy store (nanostores' mount/disabled modes): the `popstate` listener only
 * exists while something is rendering it, and is removed a second after the last
 * subscriber goes away. Nothing in a component has to remember to unbind.
 *
 * `popstate` covers back and forward only — `pushState` and `replaceState` fire
 * nothing at all — so `navigate` sets the store itself. That is the one piece of
 * bookkeeping the History API does not do for you.
 */
const $current = atom<Route>(parseRoute(currentUrl()));
onMount($current, () => {
  const sync = () => {
    $current.set(parseRoute(currentUrl()));
  };
  sync();
  window.addEventListener('popstate', sync);
  return () => {
    window.removeEventListener('popstate', sync);
  };
});

export const $route: ReadableAtom<Route> = $current;

/**
 * Navigates to a route, pushing (or replacing) a history entry.
 *
 * Safe to call from an event handler or an effect; never from render — it writes
 * to `history` and to a store, both of which are side effects.
 */
export function navigate(route: Route, options?: { readonly replace?: boolean }): void {
  const href = hrefFor(route);
  if (options?.replace === true) {
    window.history.replaceState(null, '', href);
  } else {
    window.history.pushState(null, '', href);
  }
  $current.set(parseRoute(currentUrl()));
}

const isExternalRel = (anchor: HTMLAnchorElement): boolean =>
  (anchor.getAttribute('rel') ?? '').split(/\s+/).includes('external');

/**
 * Decides what a click on a link means: a route to navigate to, or `null` for
 * "this is the browser's, do not touch it".
 *
 * The bar for intercepting is deliberately high, because every case below is a
 * thing users do on purpose and a router that swallows it is broken in a way
 * that is hard to report:
 *
 *   - a modifier key — cmd/ctrl opens a new tab, shift a new window, alt saves
 *     the target. Right-click → "open in new tab" never fires `click` at all,
 *     and middle-click fires `auxclick`, so both fall through by construction;
 *     the `button` check is belt and braces for browsers that still emit `click`.
 *   - `target` other than this document, `download`, `rel="external"`.
 *   - a cross-origin URL, or a same-origin one outside this deployment's base —
 *     a sibling app on the same host is not ours to route.
 *   - a fragment: in-page anchors are the browser's scrolling job.
 *   - an event something else already handled (`defaultPrevented`).
 *
 * Exported and pure-ish (it reads the event and `window.location`, nothing else)
 * so the decision table can be tested without a router attached.
 */
export function interceptedRoute(event: MouseEvent, base: string = BASE_PATH): Route | null {
  if (event.defaultPrevented) return null;
  if (event.button !== 0) return null;
  if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return null;

  const { target } = event;
  const anchor = target instanceof Element ? target.closest('a') : null;
  if (!(anchor instanceof HTMLAnchorElement)) return null;

  const linkTarget = anchor.getAttribute('target') ?? '';
  if (linkTarget !== '' && linkTarget !== '_self') return null;
  if (anchor.hasAttribute('download')) return null;
  if (isExternalRel(anchor)) return null;

  const href = anchor.getAttribute('href');
  if (href === null || href === '') return null;

  let url: URL;
  try {
    url = new URL(href, window.location.href);
  } catch {
    return null; // not a URL this app can own; let the browser explain it
  }
  if (url.origin !== window.location.origin) return null;
  if (url.hash !== '') return null;
  if (!isUnderBase(url.pathname, base)) return null;

  return parseRoute(url.pathname + url.search, base);
}

/**
 * Starts intercepting in-app link clicks; returns the teardown.
 *
 * One listener on `document` rather than an `onClick` on every link: it keeps
 * every in-app link a plain `<a href>` — which is what makes middle-click,
 * cmd-click, "copy link address" and the status bar preview work — and it means
 * a new component cannot forget to opt in. The listener bubbles, so anything
 * that legitimately handled the click first (a menu, a dialog) has already set
 * `defaultPrevented` and is left alone.
 */
export function interceptLinkClicks(base: string = BASE_PATH): () => void {
  const onClick = (event: MouseEvent) => {
    const route = interceptedRoute(event, base);
    if (route === null) return;
    event.preventDefault();
    navigate(route);
  };
  document.addEventListener('click', onClick);
  return () => {
    document.removeEventListener('click', onClick);
  };
}
