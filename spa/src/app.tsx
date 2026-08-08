import { useStore } from '@nanostores/react';
import { Cat, Monitor, Moon, Sun, X } from 'lucide-react';
import { lazy, Suspense, useEffect, useRef } from 'react';
import { API_BASE } from './api/client.ts';
import { BoardPage } from './pages/BoardPage.tsx';
import { BoardsPage } from './pages/BoardsPage.tsx';
import { HomePage } from './pages/HomePage.tsx';
import { PlayerPage } from './pages/PlayerPage.tsx';
import { $me, clearMe } from './state/me.ts';
import {
  $route,
  hrefFor,
  interceptLinkClicks,
  navigate,
  routeKey,
  type Route,
} from './state/router.ts';
import { $theme, setTheme, type ThemeChoice } from './state/theme.ts';
import { cn } from './ui/cn.ts';
import { ErrorBoundary } from './ui/ErrorBoundary.tsx';
import { HeaderSearch } from './ui/HeaderSearch.tsx';
import { Button, Loading, Panel } from './ui/kit/index.ts';

/**
 * The three screens that are reached deliberately rather than landed on.
 *
 * Split out of the initial bundle because each brings React Aria components
 * nothing else uses — a `Select` and a `Disclosure` per event row, a `TagGroup`
 * of chips, a `CheckboxGroup` — and a first-time visitor arriving at the front
 * page should not pay for a comparison they have not asked for. Everything they
 * *do* land on (home, boards, a board, a profile) stays in the first chunk, so
 * the common path is still one request.
 *
 * A deep link to one of these loads two chunks instead of one, which is the
 * whole of the cost and is exactly the case a static host answers from cache.
 */
const ComparePage = lazy(async () => ({
  default: (await import('./pages/ComparePage.tsx')).ComparePage,
}));
const PlayerEventsPage = lazy(async () => ({
  default: (await import('./pages/PlayerEventsPage.tsx')).PlayerEventsPage,
}));
// The global raw log brings the biggest dependency of all — React Aria's
// Virtualizer + TableLayout, which nothing else imports — so it must stay a
// lazy chunk to keep the virtualizer machinery off the critical path entirely.
const EventsPage = lazy(async () => ({
  default: (await import('./pages/EventsPage.tsx')).EventsPage,
}));
const SearchPage = lazy(async () => ({
  default: (await import('./pages/SearchPage.tsx')).SearchPage,
}));
// The stats page is a whole second screenful of tables nothing else renders,
// and it is reached on purpose rather than landed on. Same bargain as the three
// above: a deep link costs two chunks, the front page costs none of it.
const StatsPage = lazy(async () => ({
  default: (await import('./pages/StatsPage.tsx')).StatsPage,
}));

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
        className="mx-auto w-full max-w-6xl flex-1 px-4 py-6 outline-none"
      >
        {/* The fallback is a loading state, so it gets no whimsy (§9.2). */}
        {/* The boundary sits around the routed content and nothing else: the
            event pages render arbitrary server JSON, and a payload no renderer
            anticipated must cost one page view, not the shell. `resetKey`
            (not `key`) so ordinary navigation never remounts the page tree. */}
        <ErrorBoundary resetKey={key}>
          <Suspense fallback={<Loading label="Loading…" />}>
            <Screen route={route} />
          </Suspense>
        </ErrorBoundary>
      </main>
      <SiteFooter />
    </div>
  );
}

/**
 * The route table.
 *
 * An exhaustive switch over the `Route` union, so adding a route is a type error
 * until it is rendered somewhere. There is still no route-matching library here:
 * seven screens do not need one, and the point of this frontend is to show what
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
      return (
        <BoardPage
          key={`${route.stat}:${route.period}`}
          stat={route.stat}
          offset={route.offset}
          period={route.period}
        />
      );
    case 'player':
      return <PlayerPage key={route.handle} handle={route.handle} />;
    case 'playerEvents':
      // Keyed by handle only: a type-filter change is the same log narrowed,
      // and the pager re-keys itself off the route's type.
      return <PlayerEventsPage key={route.handle} handle={route.handle} type={route.type} />;
    case 'events':
      return <EventsPage type={route.type} handle={route.handle} />;
    case 'stats':
      return <StatsPage />;
    case 'search':
      return <SearchPage q={route.q} />;
    case 'compare':
      return <ComparePage handles={route.handles} />;
    case 'notFound':
      return (
        <Panel id="not-found" className="px-4 py-16 text-center">
          <h1>Nothing here</h1>
          <p id="not-found-detail" className="text-fg-muted mt-2 font-mono text-sm">
            {route.path}
          </p>
          <p className="mt-4">
            <a
              id="not-found-home"
              href={hrefFor({ name: 'home' })}
              className="text-accent-text hover:underline"
            >
              There is nothing on this page — try going back.
            </a>
          </p>
        </Panel>
      );
  }
}

function SiteHeader(props: { readonly route: Route }) {
  const active = props.route.name;
  return (
    <header className="border-border bg-canvas/85 sticky top-0 z-10 border-b backdrop-blur">
      <div className="mx-auto flex w-full max-w-6xl flex-wrap items-center gap-x-5 gap-y-2 px-4 py-2">
        <a
          href={hrefFor({ name: 'home' })}
          className="text-fg flex items-center gap-2 font-semibold"
        >
          <Cat aria-hidden className="text-accent-text size-5" />
          catlog
        </a>
        <nav aria-label="Main" className="flex items-center gap-4 text-sm">
          <NavLink route={{ name: 'home' }} label="Overview" isActive={active === 'home'} />
          <NavLink
            route={{ name: 'boards' }}
            label="Boards"
            isActive={active === 'boards' || active === 'board'}
          />
          <NavLink
            route={{ name: 'events', type: '', handle: '' }}
            label="Events"
            isActive={active === 'events'}
          />
          <NavLink
            route={{ name: 'compare', handles: [] }}
            label="Compare"
            isActive={active === 'compare'}
          />
          <NavLink route={{ name: 'stats' }} label="Stats" isActive={active === 'stats'} />
        </nav>
        {/* The search box is on every page, and it is also a real `/search?q=`
            route, so a search is a link rather than only an overlay. It starts
            as a plain input and upgrades to the suggesting combo box on first
            focus — see `HeaderSearch` for why the shell will not pay for a
            popover nobody has opened. */}
        <HeaderSearch
          label="Search handles"
          placeholder="Find a handle"
          className="order-last w-full sm:order-none sm:ml-auto sm:w-56"
          onCommit={(handle) => {
            navigate({ name: 'player', handle });
          }}
          onSubmitQuery={(q) => {
            navigate({ name: 'search', q });
          }}
          clearOnCommit
        />
        <MeChip />
        <ThemeSwitch />
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
        'hover:text-fg transition-colors duration-150',
        props.isActive ? 'text-accent-text font-medium' : 'text-fg-muted',
      )}
    >
      {props.label}
    </a>
  );
}

/**
 * `You: whiskers_prime`, with a way to let go of it.
 *
 * The clear control is here rather than only on the profile because this is the
 * one place the value is always visible, and a preference you can see but not
 * remove is a preference you resent.
 */
function MeChip() {
  const me = useStore($me);
  if (me === null) return null;
  return (
    <span className="border-accent-text/40 bg-wash-selected flex items-center gap-1 rounded-full border py-0.5 pr-1 pl-3 text-sm">
      <span className="text-fg-muted">You:</span>
      <a
        href={hrefFor({ name: 'player', handle: me })}
        className="text-fg hover:text-accent-text font-medium"
      >
        {me}
      </a>
      <Button
        variant="ghost"
        aria-label={`Forget ${me}`}
        onPress={clearMe}
        className="size-5 min-h-0 rounded-full p-0"
      >
        <X aria-hidden className="size-3.5" />
      </Button>
    </span>
  );
}

const THEMES: readonly { readonly id: ThemeChoice; readonly label: string }[] = [
  { id: 'light', label: 'Light' },
  { id: 'dark', label: 'Dark' },
  { id: 'system', label: 'Match my system' },
];

/**
 * Light, dark, or whatever the machine says — and none of them is "the real one
 * with the other bolted on" (§2.1).
 *
 * Three options rather than a two-state switch, because `system` is a real
 * answer and a switch cannot express it: a viewer whose OS flips at sunset
 * wants the page to flip with it, and there is no position of a two-way toggle
 * that means that.
 *
 * A hand-rolled radiogroup rather than React Aria's `ToggleButtonGroup`: this
 * sits in the shell, so whatever it imports is on every first paint, and three
 * mutually exclusive buttons are exactly what the WAI-ARIA radio pattern
 * specifies — `aria-checked` announces the state, the checked button is the
 * group's one tab stop, and the arrow keys move *and* select, wrapping at the
 * ends, the same keyboard behaviour the grouped toggle buttons had.
 */
function ThemeSwitch() {
  const theme = useStore($theme);
  return (
    <div
      role="radiogroup"
      aria-label="Theme"
      className="border-border bg-panel-sunken flex items-center gap-0.5 rounded-full border p-0.5"
    >
      {/* Buttons carrying the radio role — the WAI-ARIA APG radio-group
          pattern — rather than visually-hidden native inputs: this is what the
          React Aria group rendered here before it was hand-rolled, and native
          radios would put the focus ring on an invisible element. */}
      {/* oxlint-disable jsx-a11y/prefer-tag-over-role */}
      {THEMES.map((choice, index) => (
        <button
          key={choice.id}
          type="button"
          role="radio"
          aria-checked={theme === choice.id}
          aria-label={choice.label}
          tabIndex={theme === choice.id ? 0 : -1}
          onClick={() => {
            setTheme(choice.id);
          }}
          onKeyDown={(event) => {
            const delta =
              event.key === 'ArrowRight' || event.key === 'ArrowDown'
                ? 1
                : event.key === 'ArrowLeft' || event.key === 'ArrowUp'
                  ? -1
                  : 0;
            if (delta === 0) return;
            event.preventDefault();
            const next = THEMES[(index + delta + THEMES.length) % THEMES.length];
            if (next === undefined) return;
            setTheme(next.id);
            // Focus follows selection, per the radio pattern. The buttons are
            // this group's only <button> children, in THEMES order.
            event.currentTarget
              .closest('[role="radiogroup"]')
              ?.querySelectorAll('button')
              [(index + delta + THEMES.length) % THEMES.length]?.focus();
          }}
          className={cn(
            'inline-flex size-7 cursor-pointer items-center justify-center rounded-full transition-colors duration-150',
            'text-fg-muted hover:bg-wash-hover hover:text-fg active:bg-wash-press',
            'aria-checked:bg-accent aria-checked:text-accent-fg',
          )}
        >
          {choice.id === 'light' && <Sun aria-hidden className="size-3.5" />}
          {choice.id === 'dark' && <Moon aria-hidden className="size-3.5" />}
          {choice.id === 'system' && <Monitor aria-hidden className="size-3.5" />}
        </button>
      ))}
      {/* oxlint-enable jsx-a11y/prefer-tag-over-role */}
    </div>
  );
}

function SiteFooter() {
  return (
    <footer className="border-border text-fg-muted border-t px-4 py-5 text-xs">
      <div className="mx-auto flex w-full max-w-6xl flex-wrap items-center gap-x-3 gap-y-1">
        <span>Read-only view of the catlog public API.</span>
        <span className="font-mono">{API_BASE === '' ? 'same origin' : API_BASE}</span>
        <span className="text-fg-subtle">·</span>
        <span>Sign-in, handles and credentials live on the main catlog site.</span>
      </div>
    </footer>
  );
}
