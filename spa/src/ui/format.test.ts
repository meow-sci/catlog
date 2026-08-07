import { describe, expect, it } from 'vitest';
import { exactValue, formatAgo, formatInstant, formatValue } from './format.ts';

describe('formatValue', () => {
  it('shows a counter and a measurement with its unit', () => {
    expect(formatValue(214, 'm/s')).toBe('214 m/s');
    expect(formatValue(4, 'tumbles')).toBe('4 tumbles');
    expect(formatValue(6.8, 'g')).toBe('6.8 g');
  });

  it('groups thousands but keeps them exact', () => {
    expect(formatValue(9450, 'm/s')).toBe('9,450 m/s');
  });

  it('compacts a value that would otherwise blow out the column', () => {
    // distance_travelled is metres, and a real one is millions of them.
    expect(formatValue(4_210_000, 'm')).toBe('4.2M m');
    // The tooltip keeps the figure the number came from.
    expect(exactValue(4_210_000, 'm')).toBe('4210000 m');
  });

  it('never renders NaN or Infinity into the page', () => {
    expect(formatValue(Number.NaN, 'm')).toBe('—');
    expect(formatValue(Number.POSITIVE_INFINITY, 'm')).toBe('—');
  });
});

describe('formatAgo', () => {
  const now = 1_800_000_000_000;

  it.each([
    [now, 'just now'],
    [now - 10_000, 'just now'],
    [now - 5 * 60_000, '5m ago'],
    [now - 3 * 3_600_000, '3h ago'],
    [now - 4 * 86_400_000, '4d ago'],
  ])('renders %i as %s', (at, want) => {
    expect(formatAgo(at, now)).toBe(want);
  });

  it('falls back to an absolute timestamp once "ago" stops meaning anything', () => {
    expect(formatAgo(now - 400 * 86_400_000, now)).toContain('UTC');
  });

  it('never renders a negative age from a clock that is slightly ahead', () => {
    expect(formatAgo(now + 5_000, now)).toBe('just now');
  });

  it('says so rather than printing the epoch for a missing timestamp', () => {
    expect(formatAgo(0, now)).toBe('unknown');
    expect(formatInstant(0)).toBe('unknown');
  });
});

describe('formatInstant', () => {
  it("is a fixed UTC rendering — not the viewer's locale, so two people see the same string", () => {
    expect(formatInstant(1_800_000_000_000)).toBe('15 Jan 2027, 08:00 UTC');
  });
});
