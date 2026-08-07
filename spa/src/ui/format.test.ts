import { describe, expect, it } from 'vitest';
import { formatAgo, formatInstant, isoInstant } from './format.ts';

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
    expect(isoInstant(0)).toBe('');
  });
});

describe('formatInstant', () => {
  it("is a fixed UTC rendering — not the viewer's locale, so two people see the same string", () => {
    expect(formatInstant(1_800_000_000_000)).toBe('15 Jan 2027, 08:00 UTC');
  });
});
