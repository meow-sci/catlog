import { useStore } from '@nanostores/react';
import { Cat } from 'lucide-react';
import { useEffect, useRef } from 'react';
import { API_BASE } from './api/client.ts';
import { BoardPage } from './pages/BoardPage.tsx';
import { BoardsPage } from './pages/BoardsPage.tsx';
import { HomePage } from './pages/HomePage.tsx';
import { PlayerPage } from './pages/PlayerPage.tsx';
import { $route, hrefFor, interceptLinkClicks, routeKey, type Route } from './state/router.ts';
import { cn } from './ui/cn.ts';
import { Panel } from './ui/kit.tsx';

export function App() {
  const route = useStore($route);
  const key = routeKey(route);
  const main = useRef<HTMLElement>(null);

  // Every in-app `<a href>` is intercepted here, by one delegated listener, so
  // the links themselves stay plain anchors. See `interceptLinkClicks`.
  useEffect(() => interceptLinkClicks(), []);

  // What a full page load does for free, and a client-side navigation does not:
  // start at the top of the document, with focus at the top of the new content
  // rather than wherever the link that was just clicked used to be. Without the
  // focus move a screen-reader user stays parked in the old page's DOM and hears
  // nothing about the new one.
  //
  // The ref holds the key this already ran for. It is the one mutable cell in
  // the app and it exists because "did the route change?" has no other honest
  // answer in an effect: a plain first-render flag would fire spuriously under
  // StrictMode's double-invoked mount, and would then steal focus on first load.
  // Touched only from inside the effect, never during render.
  const settled = useRef(key);
  useEffect(() => {
    if (settled.current === key) return;
    settled.current = key;
    window.scrollTo({ top: 0, left: 0, behavior: 'instant' });
    main.current?.focus();
  }, [key]);

  return (
    <div className="flex min-h-dvh flex-col">
      <SiteHeader route={route} />
      {/* `tabIndex={-1}` makes this focusable by script but not by tabbing, which
          is what lets the navigation above move focus here without adding a stop
          to everybody else's tab order. */}
      <main
        ref={main}
        tabIndex={-1}
        className="mx-auto w-full max-w-5xl flex-1 px-4 py-8 outline-none"
      >
        <Screen route={route} />
      </main>
      <SiteFooter />
    </div>
  );
}

/**
 * The route table.
 *
 * An exhaustive switch over the `Route` union, so adding a route is a type error
 * until it is rendered somewhere. There is no route-matching library here: four
 * screens do not need one, and the whole point of this frontend is to show what
 * the plain version costs.
 */
function Screen(props: { readonly route: Route }) {
  const { route } = props;
  switch (route.name) {
    case 'home':
      return <HomePage />;
    case 'boards':
      return <BoardsPage />;
    case 'board':
      // Keyed so switching boards remounts rather than showing the previous
      // board's rows under the new board's title for one frame.
      return <BoardPage key={route.stat} stat={route.stat} offset={route.offset} />;
    case 'player':
      return <PlayerPage key={route.handle} handle={route.handle} />;
    case 'notFound':
      return (
        <Panel className="px-4 py-16 text-center">
          <h1 className="text-ink-50 text-xl font-semibold">Page not found</h1>
          <p className="text-ink-400 mt-2 font-mono text-sm">{route.path}</p>
        </Panel>
      );
  }
}

function SiteHeader(props: { readonly route: Route }) {
  const active = props.route.name;
  return (
    <header className="border-ink-850 bg-ink-900/70 sticky top-0 z-10 border-b backdrop-blur">
      <div className="mx-auto flex w-full max-w-5xl items-center gap-6 px-4 py-3">
        <a href={hrefFor({ name: 'home' })} className="flex items-center gap-2">
          <Cat aria-hidden className="text-flare-400 size-5" />
          <span className="text-ink-50 font-semibold tracking-tight">catlog</span>
        </a>
        <nav aria-label="Main" className="flex items-center gap-4 text-sm">
          <NavLink route={{ name: 'home' }} label="Overview" isActive={active === 'home'} />
          <NavLink
            route={{ name: 'boards' }}
            label="Boards"
            isActive={active === 'boards' || active === 'board'}
          />
        </nav>
      </div>
    </header>
  );
}

function NavLink(props: {
  readonly route: Route;
  readonly label: string;
  readonly isActive: boolean;
}) {
  return (
    <a
      href={hrefFor(props.route)}
      aria-current={props.isActive ? 'page' : undefined}
      className={cn(
        'hover:text-ink-50 transition-colors',
        props.isActive ? 'text-flare-400' : 'text-ink-400',
      )}
    >
      {props.label}
    </a>
  );
}

function SiteFooter() {
  return (
    <footer className="border-ink-850 text-ink-400 border-t px-4 py-6 text-xs">
      <div className="mx-auto flex w-full max-w-5xl flex-wrap items-center gap-x-3 gap-y-1">
        <span>Read-only view of the catlog public API.</span>
        <span className="font-mono">{API_BASE === '' ? 'same origin' : API_BASE}</span>
        <span className="text-ink-700">·</span>
        <span>Sign-in, handles and credentials live on the main catlog site.</span>
      </div>
    </footer>
  );
}
