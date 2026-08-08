import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import { stubFetch } from '../test/http.ts';
import { HeaderSearch } from './HeaderSearch.tsx';

/**
 * The deferred header search: a plain `<input type="search">` until first
 * interest, the full React Aria combo box afterwards. The rules that must hold
 * across that seam: the plain form is a working search on its own, and the
 * upgrade loses neither the letters already typed nor the focus.
 */
describe('HeaderSearch', () => {
  const noop = () => undefined;

  it('is a working search before any upgrade: Enter submits the query', async () => {
    stubFetch([{ path: '/v1/players', body: { query: 'demo', limit: 8, handles: [] } }]);
    const onSubmitQuery = vi.fn();
    const user = userEvent.setup();
    render(<HeaderSearch label="Search handles" onCommit={noop} onSubmitQuery={onSubmitQuery} />);

    await user.type(screen.getByRole('searchbox', { name: 'Search handles' }), 'demo{Enter}');
    expect(onSubmitQuery).toHaveBeenCalledWith('demo');
  });

  it('upgrades on focus, keeping the typed letters and the focus', async () => {
    stubFetch([{ path: '/v1/players', body: { query: 'de', limit: 8, handles: ['demo_ace'] } }]);
    const user = userEvent.setup();
    render(<HeaderSearch label="Search handles" onCommit={noop} onSubmitQuery={noop} />);

    const plain = screen.getByRole('searchbox', { name: 'Search handles' });
    // Set the value without focusing (fireEvent does not focus), then focus:
    // the upgrade races real typing, and whatever was in the box must survive.
    fireEvent.change(plain, { target: { value: 'de' } });
    await user.click(plain);

    // The combo box took the input's place…
    const combo = await screen.findByRole<HTMLInputElement>('combobox');
    // …holding what was typed, with focus restored.
    expect(combo.value).toBe('de');
    await waitFor(() => {
      expect(document.activeElement).toBe(combo);
    });
  });
});
