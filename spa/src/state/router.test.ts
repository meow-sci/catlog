import { afterEach, describe, expect, it } from 'vitest';
import {
  $route,
  ALLTIME,
  BASE_PATH,
  hrefFor,
  interceptedRoute,
  interceptLinkClicks,
  isUnderBase,
  navigate,
  normalizeBase,
  parseHandles,
  parseRoute,
  pathFor,
  routeKey,
  stripBase,
  type Route,
} from './router.ts';

describe('parseRoute', () => {
  it.each([
    ['/', { name: 'home' }],
    ['', { name: 'home' }],
    ['/boards', { name: 'boards' }],
    ['/boards/', { name: 'boards' }],
    ['/boards/rud_total', { name: 'board', stat: 'rud_total', offset: 0, period: ALLTIME }],
    [
      '/boards/rud_total?offset=50',
      { name: 'board', stat: 'rud_total', offset: 50, period: ALLTIME },
    ],
    ['/p/demo_ace', { name: 'player', handle: 'demo_ace' }],
    ['/stats', { name: 'stats' }],
    ['/nope', { name: 'notFound', path: '/nope' }],
  ])('parses %s', (url, want) => {
    expect(parseRoute(url, '/')).toEqual(want);
  });

  it('ignores an offset that is not a usable number', () => {
    // The address bar is user-editable; none of these may produce NaN paging.
    for (const offset of ['abc', '-10', '', 'Infinity']) {
      expect(parseRoute(`/boards/rud_total?offset=${offset}`, '/')).toEqual({
        name: 'board',
        stat: 'rud_total',
        offset: 0,
        period: ALLTIME,
      });
    }
  });

  it('drops a fragment rather than routing on it', () => {
    // No route in this app carries one, but a pasted URL can.
    expect(parseRoute('/boards/rud_total#top', '/')).toEqual({
      name: 'board',
      stat: 'rud_total',
      offset: 0,
      period: ALLTIME,
    });
  });

  it('round-trips through hrefFor at every base', () => {
    const routes: readonly Route[] = [
      { name: 'home' },
      { name: 'boards' },
      { name: 'board', stat: 'kitten_tumbles', offset: 0, period: ALLTIME },
      { name: 'board', stat: 'kitten_tumbles', offset: 100, period: ALLTIME },
      { name: 'player', handle: 'demo_crasher' },
      { name: 'stats' },
    ];
    for (const base of ['/', '/catlog/']) {
      for (const route of routes) {
        expect(parseRoute(hrefFor(route, base), base)).toEqual(route);
      }
    }
  });

  it('percent-decodes the parts a user can put anything in', () => {
    expect(parseRoute('/p/a%20b', '/')).toEqual({ name: 'player', handle: 'a b' });
    expect(hrefFor({ name: 'player', handle: 'a b' }, '/')).toBe('/p/a%20b');
  });
});

describe('base path', () => {
  it.each([
    ['', '/'],
    ['/', '/'],
    ['./', '/'],
    ['/catlog', '/catlog/'],
    ['/catlog/', '/catlog/'],
    ['https://cdn.example/catlog/', '/catlog/'],
  ])('normalizes %s to %s', (raw, want) => {
    expect(normalizeBase(raw)).toBe(want);
  });

  it('strips the base when reading a location', () => {
    expect(stripBase('/catlog/boards/rud_total', '/catlog/')).toBe('/boards/rud_total');
    // The base with no trailing slash is the app's home, not a 404.
    expect(stripBase('/catlog', '/catlog/')).toBe('/');
    expect(stripBase('/catlog/', '/catlog/')).toBe('/');
    expect(stripBase('/boards', '/')).toBe('/boards');
  });

  it('prepends the base when writing a link', () => {
    expect(hrefFor({ name: 'home' }, '/catlog/')).toBe('/catlog/');
    expect(hrefFor({ name: 'boards' }, '/catlog/')).toBe('/catlog/boards');
    expect(
      hrefFor({ name: 'board', stat: 'rud_total', offset: 50, period: ALLTIME }, '/catlog/'),
    ).toBe('/catlog/boards/rud_total?offset=50');
    expect(hrefFor({ name: 'player', handle: 'demo_ace' }, '/catlog/')).toBe('/catlog/p/demo_ace');
  });

  it('leaves a path outside the base alone, so it cannot be truncated into a real route', () => {
    // `/boards/rud_total` on a `/catlog/` deployment belongs to something else
    // on that host. Reading it as this app's board route would be a fabrication.
    expect(isUnderBase('/boards/rud_total', '/catlog/')).toBe(false);
    expect(parseRoute('/boards/rud_total', '/catlog/')).toEqual({
      name: 'notFound',
      path: '/boards/rud_total',
    });
  });

  it('defaults to the value Vite compiled in', () => {
    expect(BASE_PATH).toBe(normalizeBase(import.meta.env.BASE_URL));
    expect(hrefFor({ name: 'boards' })).toBe(hrefFor({ name: 'boards' }, BASE_PATH));
  });

  it('pathFor is the base-free half of hrefFor', () => {
    expect(pathFor({ name: 'boards' })).toBe('/boards');
    expect(pathFor({ name: 'home' })).toBe('/');
  });
});

describe('routeKey', () => {
  it('changes with the offset, so paging refetches', () => {
    expect(routeKey({ name: 'board', stat: 's', offset: 0, period: ALLTIME })).not.toBe(
      routeKey({ name: 'board', stat: 's', offset: 50, period: ALLTIME }),
    );
  });
});

/**
 * The click decision table.
 *
 * Driven through a real `document` listener and real dispatched events rather
 * than a hand-built object, because the whole question is what the DOM hands the
 * router. Every case calls `preventDefault` afterwards so nothing tries to
 * actually navigate the test environment.
 */
describe('interceptedRoute', () => {
  const cleanups: (() => void)[] = [];
  afterEach(() => {
    for (const cleanup of cleanups.splice(0)) cleanup();
  });

  /** Renders a link, clicks it with the given modifiers, returns what the router decided. */
  function clickLink(
    attributes: Readonly<Record<string, string>>,
    init: MouseEventInit = {},
  ): Route | null {
    const anchor = document.createElement('a');
    for (const [name, value] of Object.entries(attributes)) anchor.setAttribute(name, value);
    anchor.append('click me');
    document.body.append(anchor);
    cleanups.push(() => {
      anchor.remove();
    });

    let decided: Route | null = null;
    const listener = (event: Event) => {
      decided = interceptedRoute(event as MouseEvent, '/');
      // Unconditional: the test is asserting on the decision, and a real
      // navigation in happy-dom is not something to find out about here.
      event.preventDefault();
    };
    document.addEventListener('click', listener);
    cleanups.push(() => {
      document.removeEventListener('click', listener);
    });

    anchor.dispatchEvent(
      new MouseEvent('click', { bubbles: true, cancelable: true, button: 0, ...init }),
    );
    return decided;
  }

  it('intercepts a plain left-click on a same-origin in-app link', () => {
    expect(clickLink({ href: '/boards/rud_total' })).toEqual({
      name: 'board',
      stat: 'rud_total',
      offset: 0,
      period: ALLTIME,
    });
  });

  it('intercepts a click that landed on a child of the link', () => {
    const anchor = document.createElement('a');
    anchor.setAttribute('href', '/p/demo_ace');
    const icon = document.createElement('span');
    anchor.append(icon);
    document.body.append(anchor);
    cleanups.push(() => {
      anchor.remove();
    });

    let decided: Route | null = null;
    const listener = (event: Event) => {
      decided = interceptedRoute(event as MouseEvent, '/');
      event.preventDefault();
    };
    document.addEventListener('click', listener);
    cleanups.push(() => {
      document.removeEventListener('click', listener);
    });
    icon.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, button: 0 }));

    expect(decided).toEqual({ name: 'player', handle: 'demo_ace' });
  });

  it.each([
    ['cmd-click (new tab on macOS)', { metaKey: true }],
    ['ctrl-click (new tab elsewhere)', { ctrlKey: true }],
    ['shift-click (new window)', { shiftKey: true }],
    ['alt-click (save target)', { altKey: true }],
    ['middle-click', { button: 1 }],
    ['right-click', { button: 2 }],
  ])('defers %s to the browser', (_label, init: MouseEventInit) => {
    expect(clickLink({ href: '/boards/rud_total' }, init)).toBeNull();
  });

  it.each([
    ['target="_blank"', { href: '/boards', target: '_blank' }],
    ['a download', { href: '/boards', download: '' }],
    ['rel="external"', { href: '/boards', rel: 'noopener external' }],
    ['a cross-origin URL', { href: 'https://example.invalid/boards' }],
    ['an in-page fragment', { href: '#standings' }],
    ['a mailto:', { href: 'mailto:nobody@example.invalid' }],
  ])('defers %s to the browser', (_label, attributes: Record<string, string>) => {
    expect(clickLink(attributes)).toBeNull();
  });

  it('defers a click something else already handled', () => {
    const anchor = document.createElement('a');
    anchor.setAttribute('href', '/boards');
    document.body.append(anchor);
    cleanups.push(() => {
      anchor.remove();
    });

    // A menu or dialog that consumed the click first.
    const first = (event: Event) => {
      event.preventDefault();
    };
    anchor.addEventListener('click', first);

    let decided: Route | null = null;
    const listener = (event: Event) => {
      decided = interceptedRoute(event as MouseEvent, '/');
      event.preventDefault();
    };
    document.addEventListener('click', listener);
    cleanups.push(() => {
      document.removeEventListener('click', listener);
    });
    anchor.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, button: 0 }));

    expect(decided).toBeNull();
  });

  it('defers a same-origin link that belongs to a different app on the host', () => {
    const anchor = document.createElement('a');
    anchor.setAttribute('href', '/boards/rud_total');
    document.body.append(anchor);
    cleanups.push(() => {
      anchor.remove();
    });

    let decided: Route | null = null;
    const listener = (event: Event) => {
      // This deployment lives at /catlog/, so /boards/… is somebody else's.
      decided = interceptedRoute(event as MouseEvent, '/catlog/');
      event.preventDefault();
    };
    document.addEventListener('click', listener);
    cleanups.push(() => {
      document.removeEventListener('click', listener);
    });
    anchor.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, button: 0 }));

    expect(decided).toBeNull();
  });
});

/**
 * History integration.
 *
 * `$route` is a lazy store, so these subscribe to it for the duration rather
 * than calling `get()` on a disabled store.
 */
describe('$route and navigate', () => {
  const unsubscribes: (() => void)[] = [];
  afterEach(() => {
    for (const unsubscribe of unsubscribes.splice(0)) unsubscribe();
    window.history.replaceState(null, '', '/');
  });

  const watch = () => {
    const seen: Route[] = [];
    unsubscribes.push(
      $route.subscribe((route) => {
        seen.push(route);
      }),
    );
    return seen;
  };

  it('pushes a history entry and updates the store', () => {
    const seen = watch();
    navigate({ name: 'board', stat: 'rud_total', offset: 0, period: ALLTIME });

    expect(window.location.pathname).toBe(
      hrefFor({ name: 'board', stat: 'rud_total', offset: 0, period: ALLTIME }),
    );
    expect(seen.at(-1)).toEqual({ name: 'board', stat: 'rud_total', offset: 0, period: ALLTIME });
  });

  it('replaces instead of pushing when asked', () => {
    const before = window.history.length;
    watch();
    navigate({ name: 'boards' }, { replace: true });
    expect(window.history.length).toBe(before);
    expect($route.get()).toEqual({ name: 'boards' });
  });

  it('re-reads the location on popstate, which is how back and forward arrive', () => {
    const seen = watch();
    navigate({ name: 'boards' });
    expect(seen.at(-1)).toEqual({ name: 'boards' });

    // pushState fires nothing; popstate is the browser telling us it moved.
    window.history.replaceState(null, '', hrefFor({ name: 'player', handle: 'demo_ace' }));
    window.dispatchEvent(new PopStateEvent('popstate'));

    expect(seen.at(-1)).toEqual({ name: 'player', handle: 'demo_ace' });
  });

  it('carries the query string, so a page of a board is its own history entry', () => {
    watch();
    navigate({ name: 'board', stat: 'rud_total', offset: 50, period: ALLTIME });
    expect(window.location.search).toBe('?offset=50');
    expect($route.get()).toEqual({ name: 'board', stat: 'rud_total', offset: 50, period: ALLTIME });
  });
});

/** The delegated listener, end to end: a real anchor, a real click, real history. */
describe('interceptLinkClicks', () => {
  const cleanups: (() => void)[] = [];
  afterEach(() => {
    for (const cleanup of cleanups.splice(0)) cleanup();
    // Put the document back at `/` *and* tell the store about it. `replaceState`
    // fires nothing on its own — which is the whole reason `navigate` exists —
    // and nanostores keeps a store mounted for a second after its last
    // subscriber leaves, so without the popstate the next test would inherit
    // this one's route.
    window.history.replaceState(null, '', '/');
    window.dispatchEvent(new PopStateEvent('popstate'));
  });

  /**
   * Clicks a fresh `<a href>` with the interceptor installed.
   *
   * `prevented` is whether *the router* called `preventDefault`, sampled by a
   * second document listener registered after the router's — so it sees the
   * router's decision and nothing else. That listener then prevents the default
   * unconditionally, because otherwise happy-dom carries out the anchor's real
   * navigation and the next assertion is reading a different document.
   */
  function click(href: string, init: MouseEventInit = {}) {
    const stop = interceptLinkClicks('/');
    const unsubscribe = $route.subscribe(() => {});

    const anchor = document.createElement('a');
    anchor.setAttribute('href', href);
    document.body.append(anchor);

    let prevented = false;
    const observe = (event: Event) => {
      prevented = event.defaultPrevented;
      event.preventDefault();
    };
    document.addEventListener('click', observe);

    cleanups.push(() => {
      document.removeEventListener('click', observe);
      anchor.remove();
      unsubscribe();
      stop();
    });

    anchor.dispatchEvent(
      new MouseEvent('click', { bubbles: true, cancelable: true, button: 0, ...init }),
    );
    return { prevented, pathname: window.location.pathname, route: $route.get() };
  }

  it('turns a plain click on a real anchor into a history navigation', () => {
    expect(click('/boards')).toEqual({
      prevented: true,
      pathname: '/boards',
      route: { name: 'boards' },
    });
  });

  it('leaves a cmd-click for the browser, defaultPrevented and all', () => {
    // Not prevented by the router, and the current document did not move: the
    // browser is free to open its new tab.
    expect(click('/boards', { metaKey: true })).toEqual({
      prevented: false,
      pathname: '/',
      route: { name: 'home' },
    });
  });
});

describe('the new routes', () => {
  it('routes the raw event log under the profile it belongs to', () => {
    expect(parseRoute('/p/demo_ace/events', '/')).toEqual({
      name: 'playerEvents',
      handle: 'demo_ace',
      type: '',
    });
    expect(hrefFor({ name: 'playerEvents', handle: 'demo_ace', type: '' }, '/')).toBe(
      '/p/demo_ace/events',
    );
  });

  it('keeps the per-handle type filter in the URL, so back undoes a filter change', () => {
    expect(parseRoute('/p/demo_ace/events?type=vehicle.rud', '/')).toEqual({
      name: 'playerEvents',
      handle: 'demo_ace',
      type: 'vehicle.rud',
    });
    expect(hrefFor({ name: 'playerEvents', handle: 'demo_ace', type: 'vehicle.rud' }, '/')).toBe(
      '/p/demo_ace/events?type=vehicle.rud',
    );
    // The unfiltered log stays the one cacheable address.
    expect(hrefFor({ name: 'playerEvents', handle: 'demo_ace', type: '' }, '/')).toBe(
      '/p/demo_ace/events',
    );
  });

  it('routes the global raw log, filters in the URL and defaults out of it', () => {
    expect(parseRoute('/events', '/')).toEqual({ name: 'events', type: '', handle: '' });
    expect(parseRoute('/events?type=vehicle.rud&handle=demo_ace', '/')).toEqual({
      name: 'events',
      type: 'vehicle.rud',
      handle: 'demo_ace',
    });
    expect(hrefFor({ name: 'events', type: '', handle: '' }, '/')).toBe('/events');
    expect(hrefFor({ name: 'events', type: 'vehicle.rud', handle: '' }, '/')).toBe(
      '/events?type=vehicle.rud',
    );
    expect(hrefFor({ name: 'events', type: '', handle: 'demo_ace' }, '/')).toBe(
      '/events?handle=demo_ace',
    );
    // Round-trips at a subpath deployment too.
    expect(
      parseRoute(hrefFor({ name: 'events', type: 'a.b', handle: 'c' }, '/catlog/'), '/catlog/'),
    ).toEqual({ name: 'events', type: 'a.b', handle: 'c' });
  });

  it('gives the two event logs distinct fetch keys, and refetches on a filter change', () => {
    // The filter is part of the key, so changing it refetches.
    expect(routeKey({ name: 'playerEvents', handle: 'a', type: '' })).not.toBe(
      routeKey({ name: 'playerEvents', handle: 'a', type: 'vehicle.rud' }),
    );
    expect(routeKey({ name: 'events', type: '', handle: '' })).not.toBe(
      routeKey({ name: 'events', type: 'vehicle.rud', handle: '' }),
    );
    // The global log filtered to one handle is not the per-handle log's key:
    // the two endpoints answer with different envelopes.
    expect(routeKey({ name: 'events', type: '', handle: 'a' })).not.toBe(
      routeKey({ name: 'playerEvents', handle: 'a', type: '' }),
    );
  });

  it('makes a search a linkable place rather than only an overlay', () => {
    expect(parseRoute('/search?q=whisk', '/')).toEqual({ name: 'search', q: 'whisk' });
    expect(parseRoute('/search', '/')).toEqual({ name: 'search', q: '' });
    expect(hrefFor({ name: 'search', q: 'whisk ers' }, '/')).toBe('/search?q=whisk%20ers');
    // An empty query is the bare page, not `?q=`.
    expect(hrefFor({ name: 'search', q: '' }, '/')).toBe('/search');
  });

  it('keeps the compared handles in the URL, because that is the shareable part', () => {
    expect(parseRoute('/compare?handles=a,b,c', '/')).toEqual({
      name: 'compare',
      handles: ['a', 'b', 'c'],
    });
    expect(hrefFor({ name: 'compare', handles: ['a', 'b'] }, '/')).toBe('/compare?handles=a,b');
    // Nobody selected yet is a valid, empty comparison — the same request the
    // server answers with an empty body rather than an error.
    expect(parseRoute('/compare', '/')).toEqual({ name: 'compare', handles: [] });
  });

  it('accepts a repeated ?handles= the way the endpoint does', () => {
    expect(parseRoute('/compare?handles=a,b&handles=c', '/')).toEqual({
      name: 'compare',
      handles: ['a', 'b', 'c'],
    });
  });

  it('deduplicates case-insensitively and caps at the server cap', () => {
    expect(parseHandles('a, A ,b,,c')).toEqual(['a', 'b', 'c']);
    // Eight is MaxCompareHandles. The server drops the extras *silently*, so a
    // picker that let a ninth through would appear to lose it.
    expect(parseHandles('a,b,c,d,e,f,g,h,i,j')).toHaveLength(8);
  });

  it('selects a window with ?period=, and leaves the default out of the URL', () => {
    expect(parseRoute('/boards/rud_total?period=weekly', '/')).toEqual({
      name: 'board',
      stat: 'rud_total',
      offset: 0,
      period: 'weekly',
    });
    expect(hrefFor({ name: 'board', stat: 'rud_total', offset: 0, period: 'weekly' }, '/')).toBe(
      '/boards/rud_total?period=weekly',
    );
    // `alltime` is the default, so the plain URL stays the one a CDN holds.
    expect(hrefFor({ name: 'board', stat: 'rud_total', offset: 0, period: ALLTIME }, '/')).toBe(
      '/boards/rud_total',
    );
    expect(hrefFor({ name: 'board', stat: 'rud_total', offset: 50, period: 'weekly' }, '/')).toBe(
      '/boards/rud_total?offset=50&period=weekly',
    );
  });

  it('refetches when the window changes, not only when the page does', () => {
    expect(routeKey({ name: 'board', stat: 's', offset: 0, period: ALLTIME })).not.toBe(
      routeKey({ name: 'board', stat: 's', offset: 0, period: 'weekly' }),
    );
  });
});
