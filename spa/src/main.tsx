import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { App } from './app.tsx';
import './index.css';

/**
 * StrictMode is on in development, which double-invokes render and every effect.
 * That is the point: it is what surfaces an impure component before React
 * Compiler memoizes it into a bug that only reproduces on someone else's machine.
 */
const root = document.getElementById('root');
if (root === null) throw new Error('index.html has no #root element');

createRoot(root).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
