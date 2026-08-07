import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { App } from './app.tsx';
import './index.css';

/**
 * StrictMode is on in development, which double-invokes render and every effect.
 * That is the point: it is what surfaces an impure component before React
 * Compiler memoizes it into a bug that only reproduces on someone else's machine.
 */
// The app resets scroll on every route change (see `App`), so the browser must
// not also try to restore a remembered offset on back/forward: it would restore
// against a document whose rows have not been fetched yet, land in the wrong
// place, and then be overwritten. One owner of the scroll position, not two.
if ('scrollRestoration' in window.history) window.history.scrollRestoration = 'manual';

const root = document.getElementById('root');
if (root === null) throw new Error('index.html has no #root element');

createRoot(root).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
