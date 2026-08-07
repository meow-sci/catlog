import { describe, expect, it } from 'vitest';
import { hrefFor, parseRoute, routeKey } from './router.ts';

describe('parseRoute', () => {
  it.each([
    ['', { name: 'home' }],
    ['#', { name: 'home' }],
    ['#/', { name: 'home' }],
    ['#/boards', { name: 'boards' }],
    ['#/boards/', { name: 'boards' }],
    ['#/boards/rud_total', { name: 'board', stat: 'rud_total', offset: 0 }],
    ['#/boards/rud_total?offset=50', { name: 'board', stat: 'rud_total', offset: 50 }],
    ['#/p/demo_ace', { name: 'player', handle: 'demo_ace' }],
    ['#/nope', { name: 'notFound', path: '/nope' }],
  ])('parses %s', (hash, want) => {
    expect(parseRoute(hash)).toEqual(want);
  });

  it('ignores an offset that is not a usable number', () => {
    // The address bar is user-editable; none of these may produce NaN paging.
    for (const offset of ['abc', '-10', '', 'Infinity']) {
      expect(parseRoute(`#/boards/rud_total?offset=${offset}`)).toEqual({
        name: 'board',
        stat: 'rud_total',
        offset: 0,
      });
    }
  });

  it('round-trips through hrefFor', () => {
    for (const route of [
      { name: 'home' as const },
      { name: 'boards' as const },
      { name: 'board' as const, stat: 'kitten_tumbles', offset: 0 },
      { name: 'board' as const, stat: 'kitten_tumbles', offset: 100 },
      { name: 'player' as const, handle: 'demo_crasher' },
    ]) {
      expect(parseRoute(hrefFor(route))).toEqual(route);
    }
  });
});

describe('routeKey', () => {
  it('changes with the offset, so paging refetches', () => {
    expect(routeKey({ name: 'board', stat: 's', offset: 0 })).not.toBe(
      routeKey({ name: 'board', stat: 's', offset: 50 }),
    );
  });
});
