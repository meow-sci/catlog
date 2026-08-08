import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { formatValue, unitForKey } from '../units.ts';
import { EventSummary } from './EventSummary.tsx';
import { payloadFieldCount, SUMMARY_KEYS, summarizeEvent } from './summarize.ts';

/**
 * The allow-list renderer. The property under test is the same one
 * `CONTEXT_KEYS` holds: **nothing renders unless this file said so.**
 */
describe('summarizeEvent', () => {
  it('picks only the allow-listed keys, in list order, formatted by unit', () => {
    const pairs = summarizeEvent('vehicle.impact', {
      energy_j: 48_000_000,
      speed_ms: 7799,
      body: 'kerbin',
      survived: true,
      unheard_of_key: 'sees nothing',
    });
    expect(pairs.map((p) => p.key)).toEqual(['body', 'speed_ms', 'energy_j', 'survived']);
    // The exact rendering is `units.ts`'s to define; the claim here is that
    // numbers go through it (the same path as a board's context cell).
    expect(pairs.map((p) => p.value)).toEqual([
      'Kerbin',
      formatValue(7799, unitForKey('speed_ms')),
      formatValue(48_000_000, unitForKey('energy_j')),
      'true',
    ]);
  });

  it('returns nothing for an unknown type, whatever the payload holds', () => {
    // A type a newer mod version introduces must not leak values into a public
    // table merely because nobody remembered to exclude it.
    expect(summarizeEvent('brand.new_type', { speed_ms: 1, secret: 'x' })).toEqual([]);
  });

  it('skips an allow-listed key whose value is not a scalar', () => {
    // An allow-list entry is a claim about a key *and its documented shape*.
    expect(summarizeEvent('vehicle.impact', { speed_ms: { min: 1, max: 2 } })).toEqual([]);
  });

  it('does not mangle version-shaped strings', () => {
    // `titleize` splits on dots; `0.1.0` must not become `0 1 0`.
    const pairs = summarizeEvent('session.started', { mod_ver: '0.1.0', game_build: '2026.8.5' });
    expect(pairs.map((p) => p.value)).toEqual(['0.1.0', '2026.8.5']);
  });

  it('handles a payload that is not an object', () => {
    expect(summarizeEvent('vehicle.impact', null)).toEqual([]);
    expect(summarizeEvent('vehicle.impact', 'oops')).toEqual([]);
    expect(payloadFieldCount(null)).toBe(0);
  });

  it('never allow-lists the keys the server redacts or the pages hide', () => {
    // `install` is dropped server-side and must not be re-invited; `kid` is a
    // hashed identity token; ULIDs are clutter. A failure here is a review
    // conversation, not a tweak.
    for (const keys of Object.values(SUMMARY_KEYS)) {
      expect(keys).not.toContain('install');
      expect(keys).not.toContain('kid');
      expect(keys).not.toContain('other_flight');
    }
  });
});

describe('EventSummary', () => {
  it('renders the pairs for a known type', () => {
    render(<EventSummary type="vehicle.rud" payload={{ cause: 'ground_impact', peak_g: 12.5 }} />);
    expect(screen.getByText('Ground Impact')).toBeTruthy();
    expect(screen.getByText(formatValue(12.5, unitForKey('peak_g')))).toBeTruthy();
  });

  it('renders a field count — never the values — for an unknown type', () => {
    const { container } = render(
      <EventSummary
        type="totally.unknown"
        payload={{ a: 1, b: 2, c: 'secret_value', d: 4, e: 5, f: 6, g: 7 }}
      />,
    );
    expect(screen.getByText('7 fields')).toBeTruthy();
    expect(container.textContent).not.toContain('secret_value');
  });

  it('renders a dash for an empty or non-object payload', () => {
    render(<EventSummary type="totally.unknown" payload={{}} />);
    expect(screen.getByText('—')).toBeTruthy();
  });
});
